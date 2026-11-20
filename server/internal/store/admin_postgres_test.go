package store

import (
	"database/sql"
	"os"
	"testing"
)

// TestPostgresAdminListSortFilter runs the same sort/filter matrix
// as TestAdminListSortFilter against a real Postgres, gated on the
// same env var as TestPostgresStore:
//
//	DHTSEARCH_TEST_POSTGRES_URL=postgres://user:pass@host:port/dbname
//
// The admin list's parity risk is exactly what this pins: the SQL
// path's ORDER BY clauses must produce the same total order as the
// in-memory comparator the category/seeders path uses, and the size
// ranges and review-status conditions must select the same rows, on
// both dialects.
func TestPostgresAdminListSortFilter(t *testing.T) {
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

	seedAdminSortFixture(t, s)
	assertAdminSortFilter(t, s)
}
