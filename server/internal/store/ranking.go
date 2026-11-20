package store

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Relevance scoring for SearchEx's SortRelevance order.
//
// The score is computed in Go, not in SQL, on purpose. SearchEx
// already loads the full keyword match set into memory (a torrent's
// category is derived from its file list, so matches are classified
// and counted in memory anyway), so scoring adds no new unbounded
// fetch — the scored set is bounded by the AND-of-keywords WHERE,
// never the whole table. And one Go implementation is what keeps the
// order identical on SQLite and Postgres: the dialects share no
// scoring primitives (SQLite LIKE has no pg_trgm counterpart), while
// this code consumes only the row's own fields, so both backends
// rank a given match set exactly the same way.
//
// Signals, strongest first (weights in scoreField):
//   - coverage: how many of the query terms occur in the field. The
//     SQL AND-match may satisfy a term through the raw name or the
//     alias, so a display name can legitimately cover only part of
//     the query; coverage is convex (coverage^1.5) so full coverage
//     separates clearly from partial.
//   - the whole name being exactly the query phrase
//   - the full phrase occurring contiguously
//   - the name starting with the phrase (or its first term)
//   - terms occurring in query order, close together (full coverage
//     only — order is meaningless for the terms that are absent)
//   - terms matching whole words rather than substrings
//   - a length penalty: at equal match quality the shorter name
//     wins, so keyword-stuffed junk titles sink.
//
// The alias (the publisher's title) is scored with the same formula
// at a discount: it is a real title, but the display name is what
// the user scans. When the name covers nothing, the alias alone
// carries the score, so alias-only matches (romaji name, Chinese
// alias) still order among themselves.
//
// Freshness is NOT a signal: created_at only breaks exact score
// ties, and info_hash breaks those, so the order is fully
// deterministic — identical across backends and stable across
// pages of the same query.

// aliasWeight discounts the alias field against the display name.
const aliasWeight = 0.35

// queryTerms normalizes a query the way keywordWhere splits it
// (whitespace fields, capped at maxKeywords), lowercased and
// de-duplicated in first-seen order.
func queryTerms(query string) []string {
	keywords := strings.Fields(query)
	if len(keywords) > maxKeywords {
		keywords = keywords[:maxKeywords]
	}
	seen := make(map[string]bool, len(keywords))
	terms := make([]string, 0, len(keywords))
	for _, kw := range keywords {
		kw = strings.ToLower(kw)
		if !seen[kw] {
			seen[kw] = true
			terms = append(terms, kw)
		}
	}
	return terms
}

// fieldMatch is one title field's score and term coverage.
type fieldMatch struct {
	score    float64
	coverage float64
}

// scoreField scores one title (display name or alias) against the
// query terms. phrase is the terms joined by single spaces.
func scoreField(text string, terms []string, phrase string) fieldMatch {
	if text == "" || len(terms) == 0 {
		return fieldMatch{}
	}
	lower := strings.ToLower(text)
	// The squashed form (words joined by single spaces) lets the
	// phrase signals see through release-name punctuation:
	// "Movie.2024" reads as the phrase "movie 2024".
	words := strings.FieldsFunc(lower, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	squashed := strings.Join(words, " ")

	positions := make([]int, len(terms)) // byte offset of first hit, -1
	matched, whole := 0, 0
	for i, term := range terms {
		positions[i] = strings.Index(lower, term)
		if positions[i] >= 0 {
			matched++
			// Whole-word match: the term is one of the name's
			// words, not just a substring of one. Terms are
			// capped at maxKeywords, so a linear scan of the
			// word list beats building a set per scored row.
			for _, w := range words {
				if w == term {
					whole++
					break
				}
			}
		}
	}
	if matched == 0 {
		return fieldMatch{}
	}
	coverage := float64(matched) / float64(len(terms))
	// Convex coverage: 1.0 -> 100, 2/3 -> ~54, 1/2 -> ~35.
	score := 100 * coverage * (0.6 + 0.4*coverage)

	if squashed == phrase {
		score += 50 // the name IS the query
	}
	if strings.Contains(squashed, phrase) || strings.Contains(lower, phrase) {
		score += 40 // contiguous phrase (the raw form covers terms
		// that carry punctuation, e.g. "spider-man")
	}
	if strings.HasPrefix(squashed, phrase) {
		score += 20
	} else if strings.HasPrefix(lower, terms[0]) {
		score += 10
	}
	score += 4 * float64(whole)

	if matched >= 2 && coverage == 1 {
		inOrder := true
		first, last, prev := -1, -1, -1
		for _, p := range positions {
			if p < 0 {
				continue
			}
			if first < 0 {
				first = p
			}
			last = p
			if prev >= 0 && p < prev {
				inOrder = false
			}
			prev = p
		}
		if inOrder {
			score += 15
			// Proximity: the tighter the span covering all terms,
			// relative to the whole name, the better.
			score += 10 * (1 - float64(last-first)/float64(max(1, len(lower))))
		}
	}

	// Length penalty: runes beyond the phrase, capped, so a stuffed
	// title cannot bury an equally good short one.
	if extra := utf8.RuneCountInString(lower) - utf8.RuneCountInString(phrase); extra > 0 {
		pen := float64(extra) / 6
		if pen > 25 {
			pen = 25
		}
		score -= pen
	}
	return fieldMatch{score: score, coverage: coverage}
}

// relevanceScore scores a search hit for the SortRelevance order.
func relevanceScore(t Torrent, terms []string, phrase string) float64 {
	name := scoreField(t.Name, terms, phrase)
	if name.coverage == 0 {
		// Nothing in the display name: the match came through the
		// raw name hidden behind a cleaned title, or the alias
		// alone. Rank by the alias at its discount.
		return aliasWeight * scoreField(t.Alias, terms, phrase).score
	}
	alias := scoreField(t.Alias, terms, phrase)
	// The alias reinforces a name match in proportion to how much
	// of the query the name itself covers.
	return name.score + aliasWeight*name.coverage*alias.score
}

// sortByRelevance orders hits by score (descending), breaking exact
// ties by created_at (descending) and then info_hash (ascending),
// so the order is total and identical on every backend.
func sortByRelevance(hits []Torrent, terms []string) {
	phrase := strings.Join(terms, " ")
	type ranked struct {
		t     Torrent
		score float64
	}
	rs := make([]ranked, len(hits))
	for i, t := range hits {
		rs[i] = ranked{t: t, score: relevanceScore(t, terms, phrase)}
	}
	sort.SliceStable(rs, func(i, j int) bool {
		a, b := rs[i], rs[j]
		if a.score != b.score {
			return a.score > b.score
		}
		if a.t.CreatedAt != b.t.CreatedAt {
			return a.t.CreatedAt > b.t.CreatedAt
		}
		return a.t.InfoHash < b.t.InfoHash
	})
	for i := range rs {
		hits[i] = rs[i].t
	}
}
