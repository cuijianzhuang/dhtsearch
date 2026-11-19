package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestTrendingStateRoundTripSQLite(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "trending.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.LoadTrendingState(ctx); err != nil || got != nil {
		t.Fatalf("fresh store: got %q, err %v, want nil, nil", got, err)
	}
	if err := s.SaveTrendingState(ctx, []byte(`{"snapshot":{}}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveTrendingState(ctx, []byte(`{"snapshot":{"movies":["Obsession"]}}`)); err != nil {
		t.Fatal(err)
	}
	s.Close()

	// Reopen: the second write must have replaced the first.
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.LoadTrendingState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"snapshot":{"movies":["Obsession"]}}` {
		t.Fatalf("after reopen: %s", got)
	}
}

// TestPostgresTrendingState exercises the trending cache table on a
// real Postgres, gated on the same env var as TestPostgresStore:
//
//	DHTSEARCH_TEST_POSTGRES_URL=postgres://...
func TestPostgresTrendingState(t *testing.T) {
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
	if _, err := admin.Exec(`DROP TABLE IF EXISTS trending_cache`); err != nil {
		t.Fatalf("reset test database: %v", err)
	}
	admin.Close()

	s, err := OpenPostgres(url)
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	defer s.Close()
	ctx := context.Background()
	if got, err := s.LoadTrendingState(ctx); err != nil || got != nil {
		t.Fatalf("fresh store: got %q, err %v, want nil, nil", got, err)
	}
	if err := s.SaveTrendingState(ctx, []byte(`{"v":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveTrendingState(ctx, []byte(`{"v":2}`)); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadTrendingState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"v":2}` {
		t.Fatalf("postgres trending state: %s, want second write", got)
	}
}
