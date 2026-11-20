package store

import (
	"context"
	"database/sql"
	"os"
	"testing"
)

// TestPostgresSwarmStats exercises the swarm_stats table on a real
// Postgres, gated on the same env var as TestPostgresStore:
//
//	DHTSEARCH_TEST_POSTGRES_URL=postgres://user:pass@host:port/dbname
//
// The upsert's streak CASE and freshness guard read the existing
// row inside the statement; this is where the two dialects could
// plausibly diverge, so the whole state machine runs here too.
func TestPostgresSwarmStats(t *testing.T) {
	url := os.Getenv("DHTSEARCH_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("DHTSEARCH_TEST_POSTGRES_URL not set; skipping Postgres integration test")
	}
	if !IsPostgresURL(url) {
		t.Fatalf("DHTSEARCH_TEST_POSTGRES_URL is not a postgres URL")
	}
	admin, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(`DROP TABLE IF EXISTS swarm_stats`); err != nil {
		t.Fatalf("reset test database: %v", err)
	}
	admin.Close()

	s, err := OpenPostgres(url)
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	defer s.Close()
	ctx := context.Background()
	h := swarmHash('p')

	if err := s.UpsertSwarmStats(ctx, []SwarmStats{
		{InfoHash: h, Seeders: 0, Leechers: 0, Completed: 0, ScrapedAt: 100},
		{InfoHash: h, Seeders: 0, Leechers: 1, Completed: 0, ScrapedAt: 200},
	}); err != nil {
		t.Fatal(err)
	}
	m, err := s.SwarmStatsFor(ctx, []string{h})
	if err != nil {
		t.Fatal(err)
	}
	// Two rows for one hash in a single batch apply in order: the
	// streak counts both zeros and keeps the first start time.
	if got := m[h]; got.ZeroCount != 2 || got.ZeroSince != 100 || got.ScrapedAt != 200 {
		t.Fatalf("streak on Postgres = %+v, want count 2 since 100", got)
	}
	if err := s.UpsertSwarmStats(ctx, []SwarmStats{
		{InfoHash: h, Seeders: 8, Leechers: 2, Completed: 30, ScrapedAt: 300},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertSwarmStats(ctx, []SwarmStats{
		{InfoHash: h, Seeders: 0, Leechers: 0, Completed: 0, ScrapedAt: 250}, // stale
	}); err != nil {
		t.Fatal(err)
	}
	m, err = s.SwarmStatsFor(ctx, []string{h})
	if err != nil {
		t.Fatal(err)
	}
	if got := m[h]; got.Seeders != 8 || got.ZeroCount != 0 || got.ZeroSince != 0 || got.ScrapedAt != 300 {
		t.Fatalf("after recovery + stale write = %+v, want the 300 round intact", got)
	}
}
