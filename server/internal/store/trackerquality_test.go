package store

import (
	"context"
	"math"
	"path/filepath"
	"testing"
)

func TestTrackerQualityRoundTripSQLite(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "trackerquality.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}

	// Fresh store: no snapshot yet, and that is not an error.
	if got, err := s.LoadTrackerQuality(ctx, 0); err != nil || len(got) != 0 {
		t.Fatalf("fresh store: got %v, err %v, want empty, nil", got, err)
	}

	trackers := []TrackerQuality{
		{Host: "tracker.b:1337", SuccW: 5, FailW: 5, SeededW: 12, AskedW: 40, LatencyMs: 30, Attempts: 8, UpdatedAt: 2000},   // ratio 0.5
		{Host: "tracker.a:1337", SuccW: 10, FailW: 1, SeededW: 90, AskedW: 100, LatencyMs: 40, Attempts: 9, UpdatedAt: 1000}, // ratio ~0.91
		{Host: "tracker.c:1337", SuccW: 3, FailW: 1, SeededW: 0, AskedW: 25, LatencyMs: 0, Attempts: 4, UpdatedAt: 3000},     // ratio 0.75
	}
	if err := s.SaveTrackerQuality(ctx, trackers); err != nil {
		t.Fatal(err)
	}
	s.Close()

	// Reopen: the snapshot survives, ordered best ratio first,
	// with every weight intact.
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.LoadTrackerQuality(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("loaded %d trackers, want 3: %v", len(got), got)
	}
	wantOrder := []string{"tracker.a:1337", "tracker.c:1337", "tracker.b:1337"}
	for i, host := range wantOrder {
		if got[i].Host != host {
			t.Fatalf("row %d = %q, want %q (best ratio first)", i, got[i].Host, host)
		}
	}
	a := got[0]
	if math.Abs(a.SuccW-10) > 1e-9 || math.Abs(a.SeededW-90) > 1e-9 || math.Abs(a.AskedW-100) > 1e-9 ||
		math.Abs(a.LatencyMs-40) > 1e-9 || a.Attempts != 9 || a.UpdatedAt != 1000 {
		t.Fatalf("row a = %+v, weights mangled", a)
	}

	// Replace-all: a second save with a smaller set prunes the
	// trackers that fell out of it.
	if err := s.SaveTrackerQuality(ctx, trackers[:1]); err != nil {
		t.Fatal(err)
	}
	got, err = s.LoadTrackerQuality(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Host != "tracker.b:1337" {
		t.Fatalf("after replace: %v, want only tracker.b", got)
	}
}
