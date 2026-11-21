package store

import (
	"context"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// This file backs the site's anonymous search-term statistics (see
// internal/searchstats): every successful public search increments
// one counter for its normalized query, bucketed by day. The data is
// aggregate-only by construction — the schema has no column that
// could hold a client address, a user agent or anything per-visitor;
// a row is (term, day, count) plus the display casing.
//
// Rows live 90 days (the caller prunes; see PruneSearchTerms) and
// the table is copied by the cross-backend Migrate: a hot-search
// history takes weeks to accumulate and losing it in a database
// switch would reset the homepage's 热门搜索 row to blank, the same
// reasoning as fetch_attempts.

// maxSearchTermBytes caps a stored term the way the search endpoint
// caps the query itself (api.maxQueryLen): statistics never retain
// more of a query than search was willing to run.
const maxSearchTermBytes = 200

// NormalizeSearchTerm reduces a raw query to the pair the statistics
// store: term is the merge key — whitespace runs collapsed to single
// spaces (strings.Fields also trims) and ASCII letters folded to
// lower case — and display is the same collapsed text in its
// original casing, the form shown back to visitors; the most recent
// search's casing wins. Only ASCII is folded: strings.ToLower would
// also rewrite non-ASCII scripts (and can change byte lengths),
// while the point here is that "Ubuntu", "UBUNTU" and "ubuntu" are
// one search — CJK and other scripts pass through byte-identical.
// ok is false when nothing countable remains (empty or
// whitespace-only input).
func NormalizeSearchTerm(raw string) (term, display string, ok bool) {
	display = strings.Join(strings.Fields(raw), " ")
	if display == "" {
		return "", "", false
	}
	display = truncateSearchTerm(display)
	return asciiLower(display), display, true
}

// truncateSearchTerm caps s at maxSearchTermBytes on a rune
// boundary, mirroring the search endpoint's own query truncation.
func truncateSearchTerm(s string) string {
	if len(s) <= maxSearchTermBytes {
		return s
	}
	n := maxSearchTermBytes
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// asciiLower folds A-Z to a-z and leaves every other byte alone, so
// multi-byte UTF-8 sequences pass through untouched.
func asciiLower(s string) string {
	hasUpper := false
	for i := 0; i < len(s); i++ {
		if s[i] >= 'A' && s[i] <= 'Z' {
			hasUpper = true
			break
		}
	}
	if !hasUpper {
		return s
	}
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

// SearchTermDay is the day-bucket key for t: whole days since the
// Unix epoch, UTC. UTC keeps the bucket boundary identical on every
// host regardless of local timezone.
func SearchTermDay(t time.Time) int64 {
	return t.UTC().Unix() / 86400
}

// searchTermBucketKey is the row's single-column primary key: the
// day and term joined by a unit separator. The day is digits-only,
// so the encoding is injective; a single TEXT key is what the
// cross-backend Migrate's keyset copy consumes (it paginates on one
// primary-key column), which a composite (term, day) key could not
// give it.
func searchTermBucketKey(day int64, term string) string {
	return strconv.FormatInt(day, 10) + "\x1f" + term
}

// SearchTermDelta is one term's counted searches within one flush:
// how many times it was searched and the display casing of the most
// recent one.
type SearchTermDelta struct {
	Term    string
	Display string
	Count   int64
}

// RecordSearchTerms adds deltas to the given day bucket in a single
// transaction: each term's count grows by its delta and its display
// casing is replaced by the delta's (the freshest casing wins).
func (s *Store) RecordSearchTerms(ctx context.Context, day int64, deltas []SearchTermDelta) error {
	if len(deltas) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(s.d.rebind(
		`INSERT INTO search_terms (bucket_key, term, display, day, count)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(bucket_key) DO UPDATE SET
			count = count + excluded.count,
			display = excluded.display`))
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, d := range deltas {
		if _, err := stmt.ExecContext(ctx,
			searchTermBucketKey(day, d.Term), d.Term, d.Display, day, d.Count); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SearchTermStat is one row of a TopSearchTerms result: the term in
// its display casing and its total count over the window.
type SearchTermStat struct {
	Term  string `json:"term"`
	Count int64  `json:"count"`
}

// TopSearchTerms returns the limit most-searched terms over the day
// buckets from sinceDay (inclusive) onwards, ordered by total count
// descending with the term itself as a deterministic tiebreak. The
// display casing is each term's most recent one inside the window
// (its row on the latest day it was searched).
func (s *Store) TopSearchTerms(ctx context.Context, sinceDay int64, limit int) ([]SearchTermStat, error) {
	rows, err := s.queryContext(ctx,
		`SELECT term, SUM(count) AS total FROM search_terms
		 WHERE day >= ? GROUP BY term ORDER BY total DESC, term ASC LIMIT ?`,
		sinceDay, limit)
	if err != nil {
		return nil, err
	}
	var totals []SearchTermStat
	for rows.Next() {
		var st SearchTermStat
		if err := rows.Scan(&st.Term, &st.Count); err != nil {
			rows.Close()
			return nil, err
		}
		totals = append(totals, st)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if len(totals) == 0 {
		return totals, nil
	}

	// Resolve display casings: every (term, day) row in the window,
	// newest day first — the first row seen per term is its latest.
	terms := make([]interface{}, len(totals))
	for i, st := range totals {
		terms[i] = st.Term
	}
	drows, err := s.queryContext(ctx,
		`SELECT term, display FROM search_terms
		 WHERE day >= ? AND term IN (`+placeholders(len(totals))+`)
		 ORDER BY day DESC`, append([]interface{}{sinceDay}, terms...)...)
	if err != nil {
		return nil, err
	}
	defer drows.Close()
	display := make(map[string]string, len(totals))
	for drows.Next() {
		var term, disp string
		if err := drows.Scan(&term, &disp); err != nil {
			return nil, err
		}
		if _, seen := display[term]; !seen {
			display[term] = disp
		}
	}
	if err := drows.Err(); err != nil {
		return nil, err
	}
	for i := range totals {
		if disp, ok := display[totals[i].Term]; ok {
			totals[i].Term = disp
		}
	}
	return totals, nil
}

// PruneSearchTerms deletes the day buckets older than beforeDay
// (day < beforeDay) and returns how many rows were removed. The
// caller keeps 90 days: the windows the API serves are today and
// 7 days, so anything older is dead weight.
func (s *Store) PruneSearchTerms(ctx context.Context, beforeDay int64) (int64, error) {
	res, err := s.execContext(ctx, `DELETE FROM search_terms WHERE day < ?`, beforeDay)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
