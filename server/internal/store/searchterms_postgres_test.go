package store

import (
	"database/sql"
	"os"
	"testing"
)

// TestPostgresSearchTerms exercises the search_terms table on a
// real Postgres, gated on the same env var as TestPostgresStore:
//
//	DHTSEARCH_TEST_POSTGRES_URL=<redacted>
//
// The assertions are assertSearchTerms, shared with the SQLite
// test, so both backends are held to identical semantics.
func TestPostgresSearchTerms(t *testing.T) {
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
	if _, err := admin.Exec(`DROP TABLE IF EXISTS search_terms`); err != nil {
		t.Fatalf("reset test database: %v", err)
	}
	admin.Close()

	s, err := OpenPostgres(url)
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	defer s.Close()
	assertSearchTerms(t, s)
}
