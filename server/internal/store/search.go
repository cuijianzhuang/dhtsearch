package store

import (
	"context"
	"sort"
	"strings"
	"unicode"
)

// Sort orders accepted by SearchEx.
const (
	SortRelevance = "relevance" // weighted match-quality score (see ranking.go)
	SortNewest    = "newest"
	SortSize      = "size"
	SortSeeders   = "seeders" // scraped seeder count (see swarm.go)
)

// SearchParams bundles the public search's knobs. Category "" means all
// categories; Sort "" means SortRelevance.
type SearchParams struct {
	Query    string
	Category string
	Sort     string
	Page     int
	PageSize int
	// HideDead drops hits whose swarm health derives to likely_dead
	// (see SwarmHealth). Torrents with no scrape data are kept:
	// unknown is not dead.
	HideDead bool
}

// SearchEx is Search plus category filtering, per-category counts and
// alternate sort orders. Unlike Search it cannot paginate in SQL:
// a torrent's category is computed from its file list (see CategoryOf),
// so every keyword match is loaded, classified and counted before the
// category filter, sort and page are applied in memory. Keyword search
// already scans the whole table for its COUNT(*); the added cost is
// decompressing each match's file list, which the API layer bounds
// with its admission gate and per-request timeout. Counts always cover
// the full keyword match set (before the category filter), so the UI
// can label every tab, and every CategoryKeys entry is present.
func (s *Store) SearchEx(ctx context.Context, p SearchParams) (items []Torrent, total int, counts map[string]int, err error) {
	if p.Page < 1 {
		p.Page = 1
	}
	if p.PageSize < 1 {
		p.PageSize = 20
	}
	where, args := "", []interface{}{}
	if kw, kwArgs := s.keywordWhere(p.Query); kw != "" {
		where = " WHERE " + kw
		args = kwArgs
	}
	rows, err := s.queryContext(ctx, s.selectCols()+where+` ORDER BY created_at DESC`, args...)
	if err != nil {
		return nil, 0, nil, err
	}
	all, err := scanTorrents(rows)
	if err != nil {
		return nil, 0, nil, err
	}
	counts = make(map[string]int, len(CategoryKeys))
	for _, k := range CategoryKeys {
		counts[k] = 0
	}
	filtered := all[:0]
	for _, t := range all {
		counts[t.Category]++
		if p.Category == "" || t.Category == p.Category {
			filtered = append(filtered, t)
		}
	}
	// Swarm snapshots for the whole filtered set are loaded only
	// when an operation needs every hit's: sorting by seeders, or
	// dropping the likely-dead. Plain searches attach snapshots to
	// the returned page only (below), so they pay one small lookup
	// instead of one per match.
	var swarm map[string]SwarmStats
	if p.Sort == SortSeeders || p.HideDead {
		hashes := make([]string, len(filtered))
		for i, t := range filtered {
			hashes[i] = t.InfoHash
		}
		var err error
		swarm, err = s.SwarmStatsFor(ctx, hashes)
		if err != nil {
			return nil, 0, nil, err
		}
	}
	if p.HideDead {
		kept := filtered[:0]
		for _, t := range filtered {
			var sw *SwarmStats
			if v, ok := swarm[t.InfoHash]; ok {
				sw = &v
			}
			if SwarmHealth(sw) != HealthLikelyDead {
				kept = append(kept, t)
			}
		}
		filtered = kept
	}
	total = len(filtered)
	switch p.Sort {
	case SortSize:
		sort.SliceStable(filtered, func(i, j int) bool {
			if filtered[i].TotalSize != filtered[j].TotalSize {
				return filtered[i].TotalSize > filtered[j].TotalSize
			}
			return filtered[i].CreatedAt > filtered[j].CreatedAt
		})
	case SortSeeders:
		sortBySeeders(filtered, swarm)
	case SortNewest:
		// Keep the SQL order (created_at DESC).
	default: // SortRelevance (and the "" default)
		// Score the category-filtered hits in memory (see
		// ranking.go for the signals and why this is not SQL).
		// An empty query has nothing to score and keeps the SQL
		// order, so browsing stays newest-first.
		if terms := queryTerms(p.Query); len(terms) > 0 {
			sortByRelevance(filtered, terms)
		}
	}
	start := (p.Page - 1) * p.PageSize
	if start >= total {
		return []Torrent{}, total, counts, nil
	}
	end := start + p.PageSize
	if end > total {
		end = total
	}
	page := filtered[start:end]
	if swarm != nil {
		attachSwarmFrom(page, swarm)
	} else if err := s.attachSwarm(ctx, page); err != nil {
		return nil, 0, nil, err
	}
	return page, total, counts, nil
}

// Get returns one torrent by info hash (the public projection Search
// uses), or (nil, nil) when the hash is not indexed.
func (s *Store) Get(ctx context.Context, infoHash string) (*Torrent, error) {
	rows, err := s.queryContext(ctx, s.selectCols()+` WHERE info_hash = ?`, infoHash)
	if err != nil {
		return nil, err
	}
	items, err := scanTorrents(rows)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
	}
	if err := s.attachSwarm(ctx, items); err != nil {
		return nil, err
	}
	return &items[0], nil
}

// nameTokens splits a torrent name into searchable tokens: runs of
// letters/digits at least 3 runes long, de-duplicated, longest first
// (longer tokens are the more distinctive ones for similarity).
func nameTokens(name string) []string {
	seen := map[string]bool{}
	var toks []string
	for _, w := range strings.FieldsFunc(strings.ToLower(name), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if len([]rune(w)) < 3 || seen[w] {
			continue
		}
		seen[w] = true
		toks = append(toks, w)
	}
	sort.Slice(toks, func(i, j int) bool {
		return len([]rune(toks[i])) > len([]rune(toks[j]))
	})
	return toks
}

// Similar recommends torrents like the given one: candidates come from
// the ordinary keyword search over the target's most distinctive name
// tokens (no separate engine), same-category candidates rank first,
// then newest. Returns (nil, nil) when the hash is not indexed.
func (s *Store) Similar(ctx context.Context, infoHash string, limit int) ([]Torrent, error) {
	if limit <= 0 {
		limit = 6
	}
	target, err := s.Get(ctx, infoHash)
	if err != nil || target == nil {
		return nil, err
	}
	toks := nameTokens(target.Name)
	if len(toks) == 0 {
		return nil, nil
	}
	pool := map[string]Torrent{}
	var order []string
	collect := func(query string) {
		items, _, _, err := s.SearchEx(ctx, SearchParams{Query: query, Page: 1, PageSize: 50})
		if err != nil {
			return
		}
		for _, t := range items {
			if t.InfoHash == infoHash {
				continue
			}
			if _, dup := pool[t.InfoHash]; !dup {
				pool[t.InfoHash] = t
				order = append(order, t.InfoHash)
			}
		}
	}
	if len(toks) >= 2 {
		collect(toks[0] + " " + toks[1])
	}
	// Broaden token by token, most distinctive first, until the pool
	// fills: a distinctive token may occur in no other title at all
	// (a release-group name, say), while a generic one fills the list.
	for _, tok := range toks {
		if len(pool) >= limit {
			break
		}
		collect(tok)
	}
	cands := make([]Torrent, 0, len(order))
	for _, h := range order {
		cands = append(cands, pool[h])
	}
	sort.SliceStable(cands, func(i, j int) bool {
		ci, cj := cands[i].Category == target.Category, cands[j].Category == target.Category
		if ci != cj {
			return ci
		}
		return cands[i].CreatedAt > cands[j].CreatedAt
	})
	if len(cands) > limit {
		cands = cands[:limit]
	}
	return cands, nil
}
