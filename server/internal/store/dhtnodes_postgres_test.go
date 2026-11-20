package store

import (
	"context"
	"database/sql"
	"os"
	"testing"
)

// TestPostgresDHTNodes exercises the dht_nodes snapshot table on a
// real Postgres, gated on the same env var as TestPostgresStore:
//
//	DHTSEARCH_TEST_POSTGRES_URL=postgres://user:pass@host:port/dbname
func TestPostgresDHTNodes(t *testing.T) {
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
	if _, err := admin.Exec(`DROP TABLE IF EXISTS dht_nodes`); err != nil {
		t.Fatalf("reset test database: %v", err)
	}
	admin.Close()

	s, err := OpenPostgres(url)
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	defer s.Close()
	ctx := context.Background()

	if got, err := s.LoadDHTNodes(ctx, 512); err != nil || len(got) != 0 {
		t.Fatalf("fresh store: got %v, err %v", got, err)
	}
	nodes := []DHTNode{
		{Addr: "10.0.0.2:6881", SuccW: 5, FailW: 5, LatencyMs: 30.5, Attempts: 8, UpdatedAt: 2000},
		{Addr: "10.0.0.1:6881", SuccW: 10.25, FailW: 1.5, LatencyMs: 40.25, Attempts: 9, UpdatedAt: 1000},
	}
	if err := s.SaveDHTNodes(ctx, nodes); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadDHTNodes(ctx, 512)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Addr != "10.0.0.1:6881" || got[1].Addr != "10.0.0.2:6881" {
		t.Fatalf("loaded %v, want best-ratio-first [10.0.0.1, 10.0.0.2]", got)
	}
	if got[0].SuccW != 10.25 || got[0].LatencyMs != 40.25 {
		t.Fatalf("float fields did not round-trip on Postgres: %+v", got[0])
	}
	// Replace-all holds on Postgres too.
	if err := s.SaveDHTNodes(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if got, err := s.LoadDHTNodes(ctx, 512); err != nil || len(got) != 0 {
		t.Fatalf("after empty save: got %v, err %v", got, err)
	}
}
