package store

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"
)

// TestPostgresFetchAttempts exercises the fetch_attempts negative
// cache table on a real Postgres, gated on the same env var as
// TestPostgresStore:
//
//	DHTSEARCH_TEST_POSTGRES_URL=<redacted>
func TestPostgresFetchAttempts(t *testing.T) {
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
	if _, err := admin.Exec(`DROP TABLE IF EXISTS fetch_attempts`); err != nil {
		t.Fatalf("reset test database: %v", err)
	}
	admin.Close()

	s, err := OpenPostgres(url)
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	defer s.Close()
	ctx := context.Background()
	const hash = "cccccccccccccccccccccccccccccccccccccccc"
	now := time.Unix(1_700_000_000, 0)

	if stored, next, err := s.FetchAdmission(ctx, hash); err != nil || stored || next != 0 {
		t.Fatalf("fresh hash: stored=%v next=%d err=%v", stored, next, err)
	}
	nextRetry, err := s.RecordFetchFailure(ctx, hash, "timeout", now, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if want := now.Add(time.Hour).Unix(); nextRetry != want {
		t.Fatalf("nextRetry = %d, want %d", nextRetry, want)
	}
	// Second failure increments atomically on Postgres too.
	if _, err := s.RecordFetchFailure(ctx, hash, "failed", now, 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	att, found, err := s.FetchAttemptRow(ctx, hash)
	if err != nil || !found || att.Fails != 2 || att.LastOutcome != "failed" {
		t.Fatalf("row: %+v found=%v err=%v", att, found, err)
	}
	if err := s.Upsert(Torrent{InfoHash: hash, Name: "x", TotalSize: 1, CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if stored, _, err := s.FetchAdmission(ctx, hash); err != nil || !stored {
		t.Fatalf("stored check: stored=%v err=%v", stored, err)
	}
	if err := s.ClearFetchAttempt(ctx, hash); err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.FetchAttemptRow(ctx, hash); err != nil || found {
		t.Fatalf("after clear: found=%v err=%v", found, err)
	}
	if n, err := s.PruneFetchAttempts(ctx, now.Add(8*24*time.Hour).Unix()); err != nil || n != 0 {
		t.Fatalf("prune of empty table: n=%d err=%v", n, err)
	}
}
