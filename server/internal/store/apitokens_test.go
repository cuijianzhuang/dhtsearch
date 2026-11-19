package store

import (
	"context"
	"testing"
)

func TestAPITokensCRUD(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	tok, err := s.CreateAPIToken(ctx, "ci-client", "dht_ab12cd34", "hash-one", 600, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if tok.ID < 1 || !tok.Enabled || tok.RatePerMin != 600 || tok.CallCount != 0 || tok.LastUsedAt != 0 || tok.CreatedAt != 1000 {
		t.Fatalf("created token: %+v", tok)
	}
	if _, err := s.CreateAPIToken(ctx, "second", "dht_ef56gh78", "hash-two", 0, 1001); err != nil {
		t.Fatal(err)
	}
	// Duplicate hash must fail (UNIQUE).
	if _, err := s.CreateAPIToken(ctx, "dupe", "dht_ab12cd34", "hash-one", 1, 1002); err == nil {
		t.Fatal("duplicate token_hash accepted")
	}

	byHash, err := s.APITokenByHash(ctx, "hash-one")
	if err != nil || byHash == nil || byHash.ID != tok.ID {
		t.Fatalf("by hash: %v %+v", err, byHash)
	}
	if miss, err := s.APITokenByHash(ctx, "no-such-hash"); err != nil || miss != nil {
		t.Fatalf("missing hash: %v %+v", err, miss)
	}

	list, err := s.ListAPITokens(ctx)
	if err != nil || len(list) != 2 || list[0].Name != "second" {
		t.Fatalf("list: %v %+v", err, list)
	}

	// Touch accumulates and stamps.
	if err := s.TouchAPIToken(ctx, tok.ID, 2000); err != nil {
		t.Fatal(err)
	}
	if err := s.TouchAPIToken(ctx, tok.ID, 2001); err != nil {
		t.Fatal(err)
	}
	got, _ := s.APITokenByHash(ctx, "hash-one")
	if got.CallCount != 2 || got.LastUsedAt != 2001 {
		t.Fatalf("after touches: %+v", got)
	}

	// Rate update + enable/disable round-trips, with not-found reports.
	if ok, err := s.SetAPITokenRate(ctx, tok.ID, 42); !ok || err != nil {
		t.Fatalf("set rate: %v %v", ok, err)
	}
	if ok, err := s.SetAPITokenRate(ctx, 9999, 1); ok || err != nil {
		t.Fatalf("set rate missing: %v %v", ok, err)
	}
	if ok, err := s.SetAPITokenEnabled(ctx, tok.ID, false); !ok || err != nil {
		t.Fatalf("disable: %v %v", ok, err)
	}
	got, _ = s.APITokenByHash(ctx, "hash-one")
	if got.Enabled || got.RatePerMin != 42 {
		t.Fatalf("after disable/rate: %+v", got)
	}
	if ok, err := s.SetAPITokenEnabled(ctx, tok.ID, true); !ok || err != nil {
		t.Fatalf("enable: %v %v", ok, err)
	}

	if ok, err := s.DeleteAPIToken(ctx, tok.ID); !ok || err != nil {
		t.Fatalf("delete: %v %v", ok, err)
	}
	if ok, err := s.DeleteAPIToken(ctx, tok.ID); ok || err != nil {
		t.Fatalf("delete again: %v %v", ok, err)
	}
	if miss, _ := s.APITokenByHash(ctx, "hash-one"); miss != nil {
		t.Fatalf("deleted token still resolvable: %+v", miss)
	}
}
