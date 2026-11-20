package store

import (
	"context"
	"strings"
	"testing"

	"dhtsearch/server/internal/filter"
)

func swarmHash(b byte) string { return strings.Repeat(string(b), 40) }

// TestSwarmUpsertRoundTrip: a snapshot lands verbatim, a scraped
// true zero is stored and returned (distinct from a missing row),
// and a later round replaces the counters while the freshness guard
// drops a stale round that lands out of order.
func TestSwarmUpsertRoundTrip(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	h1, h2 := swarmHash('a'), swarmHash('b')

	if err := s.UpsertSwarmStats(ctx, []SwarmStats{
		{InfoHash: h1, Seeders: 12, Leechers: 3, Completed: 400, ScrapedAt: 1000},
		{InfoHash: h2, Seeders: 0, Leechers: 0, Completed: 0, ScrapedAt: 1000},
	}); err != nil {
		t.Fatal(err)
	}
	m, err := s.SwarmStatsFor(ctx, []string{h1, h2, swarmHash('c')})
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 2 {
		t.Fatalf("got %d rows, want 2 (missing hash must stay absent)", len(m))
	}
	if got := m[h1]; got.Seeders != 12 || got.Leechers != 3 || got.Completed != 400 ||
		got.ScrapedAt != 1000 || got.ZeroSince != 0 || got.ZeroCount != 0 {
		t.Fatalf("h1 = %+v", got)
	}
	// The all-zero row exists: unknown (absent) and scraped-zero are
	// different answers.
	if got := m[h2]; got.Seeders != 0 || got.ZeroCount != 1 || got.ZeroSince != 1000 {
		t.Fatalf("h2 zero row = %+v, want seeders 0 with a fresh streak", got)
	}

	// A fresher round replaces the counters.
	if err := s.UpsertSwarmStats(ctx, []SwarmStats{
		{InfoHash: h1, Seeders: 20, Leechers: 5, Completed: 450, ScrapedAt: 2000},
	}); err != nil {
		t.Fatal(err)
	}
	// A stale round (older scraped_at landing late, as concurrent
	// scrape workers can produce) must be ignored entirely.
	if err := s.UpsertSwarmStats(ctx, []SwarmStats{
		{InfoHash: h1, Seeders: 1, Leechers: 1, Completed: 1, ScrapedAt: 1500},
	}); err != nil {
		t.Fatal(err)
	}
	m, err = s.SwarmStatsFor(ctx, []string{h1})
	if err != nil {
		t.Fatal(err)
	}
	if got := m[h1]; got.Seeders != 20 || got.Completed != 450 || got.ScrapedAt != 2000 {
		t.Fatalf("after stale write h1 = %+v, want the 2000 round intact", got)
	}
}

// TestSwarmZeroStreak walks the streak state machine: zeros
// accumulate under the streak's original start time, a seeder
// sighting clears the streak, and the next zero starts a new one.
func TestSwarmZeroStreak(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	h := swarmHash('z')

	up := func(seeders, at int64) SwarmStats {
		t.Helper()
		if err := s.UpsertSwarmStats(ctx, []SwarmStats{
			{InfoHash: h, Seeders: seeders, ScrapedAt: at},
		}); err != nil {
			t.Fatal(err)
		}
		m, err := s.SwarmStatsFor(ctx, []string{h})
		if err != nil {
			t.Fatal(err)
		}
		return m[h]
	}

	if got := up(0, 100); got.ZeroCount != 1 || got.ZeroSince != 100 {
		t.Fatalf("first zero: %+v, want count 1 since 100", got)
	}
	if got := up(0, 200); got.ZeroCount != 2 || got.ZeroSince != 100 {
		t.Fatalf("second zero: %+v, want count 2 since 100 (streak start kept)", got)
	}
	if got := up(0, 300); got.ZeroCount != 3 || got.ZeroSince != 100 {
		t.Fatalf("third zero: %+v, want count 3 since 100", got)
	}
	if got := up(7, 400); got.ZeroCount != 0 || got.ZeroSince != 0 || got.Seeders != 7 {
		t.Fatalf("seeder sighting: %+v, want streak cleared", got)
	}
	if got := up(0, 500); got.ZeroCount != 1 || got.ZeroSince != 500 {
		t.Fatalf("zero after recovery: %+v, want a new streak at 500", got)
	}
	// A stale zero must not extend the streak either.
	if got := up(0, 450); got.ZeroCount != 1 || got.ZeroSince != 500 || got.ScrapedAt != 500 {
		t.Fatalf("stale zero: %+v, want the 500 round untouched", got)
	}
}

// TestSwarmHealthBranches pins every derivation branch, including
// both halves of the likely_dead conjunction (streak length and
// span must both hold).
func TestSwarmHealthBranches(t *testing.T) {
	const hour = 3600
	cases := []struct {
		name string
		in   *SwarmStats
		want string
	}{
		{"no data", nil, HealthUnknown},
		{"seeders present", &SwarmStats{Seeders: 1}, HealthAlive},
		{"seeders with old zero streak", &SwarmStats{Seeders: 2, ZeroCount: 9, ZeroSince: 10, ScrapedAt: 10 + 100*hour}, HealthAlive},
		{"leechers only", &SwarmStats{Seeders: 0, Leechers: 4, ZeroCount: 9, ZeroSince: 10, ScrapedAt: 10 + 100*hour}, HealthNoSeed},
		{"first zero", &SwarmStats{ZeroCount: 1, ZeroSince: 1000, ScrapedAt: 1000}, HealthSuspect},
		{"long streak short span", &SwarmStats{ZeroCount: 5, ZeroSince: 1000, ScrapedAt: 1000 + 47*hour}, HealthSuspect},
		{"long span short streak", &SwarmStats{ZeroCount: 2, ZeroSince: 1000, ScrapedAt: 1000 + 96*hour}, HealthSuspect},
		{"dead at the boundary", &SwarmStats{ZeroCount: 3, ZeroSince: 1000, ScrapedAt: 1000 + 48*hour}, HealthLikelyDead},
		{"long dead", &SwarmStats{ZeroCount: 12, ZeroSince: 1000, ScrapedAt: 1000 + 30*24*hour}, HealthLikelyDead},
	}
	for _, c := range cases {
		if got := SwarmHealth(c.in); got != c.want {
			t.Errorf("%s: health = %q, want %q", c.name, got, c.want)
		}
	}
}

func seedSwarmSearch(t *testing.T, s *Store) (alive, dead, unknown string) {
	t.Helper()
	alive, dead, unknown = swarmHash('a'), swarmHash('d'), swarmHash('u')
	for _, tt := range []Torrent{
		{InfoHash: alive, Name: "Swarm Test Movie", TotalSize: 100, FileCount: 1,
			Files: []filter.File{{Path: "a.mkv", Size: 100}}, CreatedAt: 100},
		{InfoHash: dead, Name: "Swarm Test Dead", TotalSize: 100, FileCount: 1,
			Files: []filter.File{{Path: "d.mkv", Size: 100}}, CreatedAt: 300},
		{InfoHash: unknown, Name: "Swarm Test Unknown", TotalSize: 100, FileCount: 1,
			Files: []filter.File{{Path: "u.mkv", Size: 100}}, CreatedAt: 200},
	} {
		if err := s.Upsert(tt); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	// alive: 50 seeders. dead: three zero scrapes spanning 3 days.
	if err := s.UpsertSwarmStats(ctx, []SwarmStats{
		{InfoHash: alive, Seeders: 50, Leechers: 2, Completed: 900, ScrapedAt: 1000},
		{InfoHash: dead, Seeders: 0, Leechers: 0, Completed: 3, ScrapedAt: 1000},
	}); err != nil {
		t.Fatal(err)
	}
	for _, at := range []int64{1000 + 24*3600, 1000 + 72*3600} {
		if err := s.UpsertSwarmStats(ctx, []SwarmStats{
			{InfoHash: dead, Seeders: 0, Leechers: 0, Completed: 3, ScrapedAt: at},
		}); err != nil {
			t.Fatal(err)
		}
	}
	return alive, dead, unknown
}

// TestSearchExSortSeeders: seeders descending, a scraped true zero
// above the never-scraped, and the page carrying its snapshots.
func TestSearchExSortSeeders(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	alive, dead, unknown := seedSwarmSearch(t, s)

	items, total, _, err := s.SearchEx(context.Background(), SearchParams{
		Query: "Swarm", Sort: SortSeeders, Page: 1, PageSize: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || len(items) != 3 {
		t.Fatalf("total %d len %d, want 3/3", total, len(items))
	}
	want := []string{alive, dead, unknown} // 50 seeders, scraped 0, never scraped
	for i, h := range want {
		if items[i].InfoHash != h {
			t.Fatalf("position %d = %s, want %s (full order %v)", i, items[i].InfoHash, h,
				[]string{items[0].InfoHash, items[1].InfoHash, items[2].InfoHash})
		}
	}
	if items[0].Swarm == nil || items[0].Swarm.Seeders != 50 {
		t.Fatalf("alive item swarm = %+v", items[0].Swarm)
	}
	if items[1].Swarm == nil || items[1].Swarm.Seeders != 0 {
		t.Fatalf("dead item swarm = %+v, want a stored zero snapshot", items[1].Swarm)
	}
	if items[2].Swarm != nil {
		t.Fatalf("unknown item swarm = %+v, want nil", items[2].Swarm)
	}
}

// TestSearchExHideDead: likely_dead is filtered, unknown is kept
// (unknown is not dead), and the default keeps everything.
func TestSearchExHideDead(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	alive, _, unknown := seedSwarmSearch(t, s)
	ctx := context.Background()

	items, total, _, err := s.SearchEx(ctx, SearchParams{
		Query: "Swarm", Page: 1, PageSize: 10, HideDead: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("hide_dead: total %d len %d, want 2/2", total, len(items))
	}
	for _, it := range items {
		if SwarmHealth(it.Swarm) == HealthLikelyDead {
			t.Fatalf("hide_dead kept a likely_dead item: %s", it.InfoHash)
		}
	}
	_ = alive
	_ = unknown

	_, total, _, err = s.SearchEx(ctx, SearchParams{Query: "Swarm", Page: 1, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 {
		t.Fatalf("default search total = %d, want 3 (nothing hidden)", total)
	}
}

// TestGetAndLatestAttachSwarm covers the two other read paths the
// API serves swarm data through.
func TestGetAndLatestAttachSwarm(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	alive, _, _ := seedSwarmSearch(t, s)
	ctx := context.Background()

	got, err := s.Get(ctx, alive)
	if err != nil || got == nil {
		t.Fatalf("Get: %v %v", got, err)
	}
	if got.Swarm == nil || got.Swarm.Seeders != 50 || SwarmHealth(got.Swarm) != HealthAlive {
		t.Fatalf("Get swarm = %+v", got.Swarm)
	}
	latest, err := s.Latest(ctx, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	var withSwarm int
	for _, it := range latest {
		if it.Swarm != nil {
			withSwarm++
		}
	}
	if withSwarm != 2 {
		t.Fatalf("Latest attached %d snapshots, want 2 of 3", withSwarm)
	}
}
