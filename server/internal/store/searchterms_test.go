package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestNormalizeSearchTerm(t *testing.T) {
	cases := []struct {
		raw        string
		term, disp string
		ok         bool
	}{
		{"ubuntu", "ubuntu", "ubuntu", true},
		{"Ubuntu", "ubuntu", "Ubuntu", true},
		{"  UBUNTU   Desktop  ", "ubuntu desktop", "UBUNTU Desktop", true},
		{"a\t b\n\nc", "a b c", "a b c", true},
		{"肖申克的救赎", "肖申克的救赎", "肖申克的救赎", true},
		{"The Shawshank Redemption", "the shawshank redemption", "The Shawshank Redemption", true},
		{"", "", "", false},
		{"   \t\n ", "", "", false},
		// Ideographic spaces are whitespace too (strings.Fields).
		{"a　b", "a b", "a b", true},
	}
	for _, c := range cases {
		term, disp, ok := NormalizeSearchTerm(c.raw)
		if term != c.term || disp != c.disp || ok != c.ok {
			t.Errorf("NormalizeSearchTerm(%q) = (%q, %q, %v), want (%q, %q, %v)",
				c.raw, term, disp, ok, c.term, c.disp, c.ok)
		}
	}
	// The byte cap mirrors the search endpoint's query cap.
	term, _, ok := NormalizeSearchTerm(strings.Repeat("a", 300))
	if !ok || len(term) != maxSearchTermBytes {
		t.Errorf("overlong term: ok=%v len=%d, want len %d", ok, len(term), maxSearchTermBytes)
	}
	// The cap lands on a rune boundary, never mid-character.
	term, _, _ = NormalizeSearchTerm(strings.Repeat("é", 150)) // 300 bytes
	if len(term) > maxSearchTermBytes || len(term)%2 != 0 {
		t.Errorf("rune-boundary cap: len=%d", len(term))
	}
}

// assertSearchTerms runs the search_terms behaviour suite against
// any backend; the SQLite test and the Postgres integration test
// share it so both dialects are held to identical semantics.
func assertSearchTerms(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	// Fixed UTC days: 2026-11-21 is day 20388 (arbitrary but stable).
	day := SearchTermDay(time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC))

	record := func(d int64, deltas ...SearchTermDelta) {
		t.Helper()
		if err := s.RecordSearchTerms(ctx, d, deltas); err != nil {
			t.Fatalf("RecordSearchTerms(day %d): %v", d, err)
		}
	}
	top := func(since int64, limit int) []SearchTermStat {
		t.Helper()
		got, err := s.TopSearchTerms(ctx, since, limit)
		if err != nil {
			t.Fatalf("TopSearchTerms(since %d): %v", since, err)
		}
		return got
	}

	// Case/whitespace variants of one query merge under the
	// normalized term; the display casing is the most recent one.
	termA, dispA, _ := NormalizeSearchTerm("Ubuntu")
	termB, dispB, _ := NormalizeSearchTerm("  UBUNTU ")
	if termA != termB {
		t.Fatalf("variants did not merge: %q vs %q", termA, termB)
	}
	record(day, SearchTermDelta{Term: termA, Display: dispA, Count: 2})
	record(day, SearchTermDelta{Term: termB, Display: dispB, Count: 3})
	record(day,
		SearchTermDelta{Term: "shawshank", Display: "Shawshank", Count: 4},
		SearchTermDelta{Term: "dune", Display: "Dune", Count: 1},
	)
	// An older day for ubuntu and shawshank; a day outside the 7d
	// window for an otherwise-hot term.
	record(day-3, SearchTermDelta{Term: termA, Display: "ubuntu", Count: 10})
	record(day-6, SearchTermDelta{Term: "shawshank", Display: "shawshank", Count: 1})
	record(day-30, SearchTermDelta{Term: "ancient", Display: "Ancient", Count: 999})

	// Today window: only today's buckets, counts as recorded today,
	// display = today's latest casing.
	got := top(day, 10)
	want := []SearchTermStat{
		{Term: "UBUNTU", Count: 5},
		{Term: "Shawshank", Count: 4},
		{Term: "Dune", Count: 1},
	}
	if len(got) != len(want) {
		t.Fatalf("today top = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("today top[%d] = %+v, want %+v (all: %+v)", i, got[i], want[i], got)
		}
	}

	// 7d window: sums across buckets; display from the LATEST day
	// in the window (ubuntu shows today's "UBUNTU", not day-3's
	// lowercase); the 30-day-old term stays out.
	got = top(day-6, 10)
	want = []SearchTermStat{
		{Term: "UBUNTU", Count: 15},
		{Term: "Shawshank", Count: 5},
		{Term: "Dune", Count: 1},
	}
	if len(got) != len(want) {
		t.Fatalf("7d top = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("7d top[%d] = %+v, want %+v (all: %+v)", i, got[i], want[i], got)
		}
	}

	// Limit applies after ranking.
	if got := top(day-6, 2); len(got) != 2 || got[0].Count != 15 {
		t.Fatalf("limited top = %+v", got)
	}

	// Pruning removes buckets older than the cutoff and nothing else.
	n, err := s.PruneSearchTerms(ctx, day-6)
	if err != nil {
		t.Fatalf("PruneSearchTerms: %v", err)
	}
	if n != 1 { // only the day-30 row
		t.Fatalf("pruned %d rows, want 1", n)
	}
	if got := top(day-6, 10); len(got) != 3 {
		t.Fatalf("top after prune = %+v, want the 3 in-window terms", got)
	}
	if n, err := s.PruneSearchTerms(ctx, day+1); err != nil || n != 5 {
		t.Fatalf("prune-all: n=%d err=%v, want 5 rows removed", n, err)
	}
	if got := top(day-6, 10); len(got) != 0 {
		t.Fatalf("top after prune-all = %+v, want empty", got)
	}
}

func TestSearchTerms(t *testing.T) {
	assertSearchTerms(t, openTest(t))
}

func TestSearchTermDayBoundaries(t *testing.T) {
	// Same instant, two zones: the bucket follows UTC.
	ts := time.Date(2026, 11, 21, 1, 30, 0, 0, time.FixedZone("CST", 8*3600))
	if got, want := SearchTermDay(ts), SearchTermDay(ts.UTC()); got != want {
		t.Fatalf("SearchTermDay zone mismatch: %d vs %d", got, want)
	}
	// Midnight UTC starts a new bucket.
	if SearchTermDay(time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC)) ==
		SearchTermDay(time.Date(2026, 11, 20, 23, 59, 59, 0, time.UTC)) {
		t.Fatal("midnight UTC did not roll the bucket")
	}
}
