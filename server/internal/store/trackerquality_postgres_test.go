package store

import (
	"context"
	"database/sql"
	"math"
	"os"
	"testing"
)

// TestPostgresTrackerQuality exercises the tracker_quality
// snapshot table on a real Postgres, gated on the same env var as
// TestPostgresStore:
//
//	DHTSEARCH_TEST_POSTGRES_URL=postgres://user:pass@host:5432/db
func TestPostgresTrackerQuality(t *testing.T) {
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
	if _, err := admin.Exec(`DROP TABLE IF EXISTS tracker_quality`); err != nil {
		t.Fatalf("reset test database: %v", err)
	}
	admin.Close()

	s, err := OpenPostgres(url)
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	defer s.Close()
	ctx := context.Background()

	if got, err := s.LoadTrackerQuality(ctx, 0); err != nil || len(got) != 0 {
		t.Fatalf("fresh store: got %v, err %v", got, err)
	}
	trackers := []TrackerQuality{
		{Host: "tracker.b:1337", SuccW: 5, FailW: 5, SeededW: 12.5, AskedW: 40.25, LatencyMs: 30.5, Attempts: 8, UpdatedAt: 2000},
		{Host: "tracker.a:1337", SuccW: 10.25, FailW: 1.5, SeededW: 90.5, AskedW: 100.75, LatencyMs: 40.25, Attempts: 9, UpdatedAt: 1000},
	}
	if err := s.SaveTrackerQuality(ctx, trackers); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadTrackerQuality(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Host != "tracker.a:1337" {
		t.Fatalf("loaded %v, want tracker.a first (best ratio)", got)
	}
	if math.Abs(got[0].SeededW-90.5) > 1e-9 || math.Abs(got[0].AskedW-100.75) > 1e-9 {
		t.Fatalf("weights mangled on Postgres: %+v", got[0])
	}
	// Replace-all prunes like on SQLite.
	if err := s.SaveTrackerQuality(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if got, err := s.LoadTrackerQuality(ctx, 0); err != nil || len(got) != 0 {
		t.Fatalf("after empty save: got %v, err %v, want empty", got, err)
	}
}
