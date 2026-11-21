package store

import (
	"database/sql"
	"os"
	"testing"
)

// TestPostgresStalestSwarmHashes runs the re-scrape selection
// assertions on a real Postgres, gated on the same env var as
// TestPostgresStore:
//
//	DHTSEARCH_TEST_POSTGRES_URL=<redacted>
//
// The selection is a LEFT JOIN with a COALESCE ordering — plain
// SQL on paper, but exactly the kind of statement whose NULL
// ordering could plausibly diverge between dialects, so it runs
// here too.
func TestPostgresStalestSwarmHashes(t *testing.T) {
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
	assertStalestSelection(t, s)
}
