package store

import (
	"database/sql"
	"os"
	"testing"
)

// TestPostgresFilesPage runs the same paging contract as TestFilesPage
// against a real Postgres when DHTSEARCH_TEST_POSTGRES_URL points at a
// throwaway database (the test drops and recreates the torrents table).
func TestPostgresFilesPage(t *testing.T) {
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
	if _, err := admin.Exec(`DROP TABLE IF EXISTS torrents`); err != nil {
		t.Fatalf("reset test database: %v", err)
	}
	admin.Close()

	s, err := OpenPostgres(url)
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	defer s.Close()
	hash := seedFilesTorrent(t, s, 250)
	checkFilesPage(t, s, hash)
}
