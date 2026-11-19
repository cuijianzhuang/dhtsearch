package store

import (
	"context"
	"database/sql"
	"os"
	"testing"
)

// TestPostgresModerationLog exercises the moderation audit log on a
// real Postgres (BIGSERIAL id, prune subquery), gated on the same env
// var as TestPostgresStore:
//
//	DHTSEARCH_TEST_POSTGRES_URL=postgres://user:pass@host:5432/dbname
func TestPostgresModerationLog(t *testing.T) {
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
	if _, err := admin.Exec(`DROP TABLE IF EXISTS moderation_log`); err != nil {
		t.Fatalf("reset test database: %v", err)
	}
	admin.Close()

	s, err := OpenPostgres(url)
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	defer s.Close()
	ctx := context.Background()

	recs := []ModerationRecord{
		{InfoHash: "h1", Name: "Clean Movie", Label: "ok", Action: "kept"},
		{InfoHash: "h2", Name: "Bad Movie", Label: "adult", Action: "deleted"},
	}
	if err := s.LogModeration(recs, 99); err != nil {
		t.Fatal(err)
	}
	got, err := s.RecentModeration(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].InfoHash != "h2" || got[1].CreatedAt != 99 {
		t.Fatalf("recent = %+v", got)
	}
}
