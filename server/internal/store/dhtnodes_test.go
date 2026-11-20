package store

import (
	"context"
	"math"
	"path/filepath"
	"testing"
)

func TestDHTNodesRoundTripSQLite(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "nodes.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}

	// Fresh store: no snapshot yet, and that is not an error.
	if got, err := s.LoadDHTNodes(ctx, 512); err != nil || len(got) != 0 {
		t.Fatalf("fresh store: got %v, err %v, want empty, nil", got, err)
	}

	nodes := []DHTNode{
		{Addr: "10.0.0.2:6881", SuccW: 5, FailW: 5, LatencyMs: 30, Attempts: 8, UpdatedAt: 2000},  // ratio 0.5
		{Addr: "10.0.0.1:6881", SuccW: 10, FailW: 1, LatencyMs: 40, Attempts: 9, UpdatedAt: 1000}, // ratio ~0.91
		{Addr: "10.0.0.3:6881", SuccW: 3, FailW: 1, LatencyMs: 0, Attempts: 4, UpdatedAt: 3000},   // ratio 0.75
	}
	if err := s.SaveDHTNodes(ctx, nodes); err != nil {
		t.Fatal(err)
	}
	s.Close()

	// Reopen: the snapshot survives, ordered best ratio first.
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.LoadDHTNodes(ctx, 512)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("loaded %d nodes, want 3", len(got))
	}
	wantOrder := []string{"10.0.0.1:6881", "10.0.0.3:6881", "10.0.0.2:6881"}
	for i, want := range wantOrder {
		if got[i].Addr != want {
			t.Fatalf("loaded[%d]=%s, want %s (best success ratio first)", i, got[i].Addr, want)
		}
	}
	if math.Abs(got[0].SuccW-10) > 1e-9 || math.Abs(got[0].LatencyMs-40) > 1e-9 ||
		got[0].Attempts != 9 || got[0].UpdatedAt != 1000 {
		t.Fatalf("loaded[0]=%+v, fields did not round-trip", got[0])
	}

	// The limit is honoured.
	if got, err := s.LoadDHTNodes(ctx, 2); err != nil || len(got) != 2 {
		t.Fatalf("limit 2: got %v, err %v", got, err)
	}

	// Replace-all: saving a smaller snapshot drops the nodes that
	// fell out of the top set instead of accumulating them.
	if err := s.SaveDHTNodes(ctx, nodes[:1]); err != nil {
		t.Fatal(err)
	}
	got, err = s.LoadDHTNodes(ctx, 512)
	if err != nil || len(got) != 1 || got[0].Addr != "10.0.0.2:6881" {
		t.Fatalf("after replace: got %v, err %v, want only 10.0.0.2", got, err)
	}

	// Saving an empty snapshot clears the table (the crawler only
	// does this from a live tracker that genuinely has no proven
	// nodes left).
	if err := s.SaveDHTNodes(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if got, err := s.LoadDHTNodes(ctx, 512); err != nil || len(got) != 0 {
		t.Fatalf("after empty save: got %v, err %v", got, err)
	}
}
