package store

import (
	"context"
	"database/sql"
	"os"
	"testing"
)

// TestPostgresAPITokens exercises the api_tokens table on a real
// Postgres, gated on the same env var as TestPostgresStore:
//
//	DHTSEARCH_TEST_POSTGRES_URL=postgres://user:pass@host:5432/db
func TestPostgresAPITokens(t *testing.T) {
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
	if _, err := admin.Exec(`DROP TABLE IF EXISTS api_tokens`); err != nil {
		t.Fatalf("reset test database: %v", err)
	}
	admin.Close()

	s, err := OpenPostgres(url)
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	defer s.Close()
	ctx := context.Background()

	tok, err := s.CreateAPIToken(ctx, "pg-client", "dht_pg123456", "pg-hash", 300, 5000)
	if err != nil {
		t.Fatal(err)
	}
	if tok.ID < 1 {
		t.Fatalf("postgres token id: %+v", tok)
	}
	if err := s.TouchAPIToken(ctx, tok.ID, 5001); err != nil {
		t.Fatal(err)
	}
	got, err := s.APITokenByHash(ctx, "pg-hash")
	if err != nil || got == nil || got.CallCount != 1 || got.LastUsedAt != 5001 || got.RatePerMin != 300 {
		t.Fatalf("postgres by hash: %v %+v", err, got)
	}
	if ok, err := s.SetAPITokenEnabled(ctx, tok.ID, false); !ok || err != nil {
		t.Fatalf("postgres disable: %v %v", ok, err)
	}
	list, err := s.ListAPITokens(ctx)
	if err != nil || len(list) != 1 || list[0].Enabled {
		t.Fatalf("postgres list: %v %+v", err, list)
	}
	if ok, err := s.DeleteAPIToken(ctx, tok.ID); !ok || err != nil {
		t.Fatalf("postgres delete: %v %v", ok, err)
	}
}
