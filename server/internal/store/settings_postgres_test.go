package store

import (
	"context"
	"database/sql"
	"os"
	"testing"
)

// TestPostgresSettings exercises the settings table on a real
// Postgres, gated on the same env var as TestPostgresStore:
//
//	DHTSEARCH_TEST_POSTGRES_URL=postgres://... go test ./internal/store/ -run TestPostgresSettings -v
func TestPostgresSettings(t *testing.T) {
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
	if _, err := admin.Exec(`DROP TABLE IF EXISTS settings`); err != nil {
		t.Fatalf("reset test database: %v", err)
	}
	admin.Close()

	s, err := OpenPostgres(url)
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	defer s.Close()
	ctx := context.Background()
	if err := s.SaveSettings(ctx, map[string]string{"META_TIMEOUT": "30s", "OPENAI_API_KEY": "k"}, nil, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSettings(ctx, map[string]string{"META_TIMEOUT": "45s"}, []string{"OPENAI_API_KEY"}, 2); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["META_TIMEOUT"] != "45s" {
		t.Fatalf("postgres settings: %v, want only META_TIMEOUT=45s", got)
	}
}
