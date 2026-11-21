package store

import (
	"database/sql"
	"os"
	"testing"
)

// TestPostgresRetentionCleanup runs the same eligibility and
// deletion matrix as TestRetentionCleanup against a real Postgres,
// gated on the same env var as TestPostgresStore:
//
//	DHTSEARCH_TEST_POSTGRES_URL=<redacted>
//
// The parity risk is exactly what this pins: the candidate
// prefilter's JOIN and keyset walk, the three-table delete
// transaction and the stats writes must behave identically on both
// dialects — a cleanup that deletes the wrong rows on one backend
// is the worst failure mode this feature has.
func TestPostgresRetentionCleanup(t *testing.T) {
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
	for _, stmt := range []string{
		`DROP TABLE IF EXISTS torrents`, `DROP TABLE IF EXISTS swarm_stats`,
		`DROP TABLE IF EXISTS fetch_attempts`, `DROP TABLE IF EXISTS blocked`,
		`DROP TABLE IF EXISTS stats`,
	} {
		if _, err := admin.Exec(stmt); err != nil {
			t.Fatalf("reset test database: %v", err)
		}
	}
	admin.Close()

	s, err := OpenPostgres(url)
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	defer s.Close()

	assertRetentionBehavior(t, s)
}
