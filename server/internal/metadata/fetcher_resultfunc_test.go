package metadata

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestResultFuncOutcomes pins the ResultFunc contract the admission
// negative cache relies on: a malformed hash reports OutcomeFailed,
// and a valid hash nobody serves reports OutcomePrescreened once the
// prescreen window closes (the fetcher's clients have no DHT and the
// magnet carries no trackers, so no peer can ever connect).
func TestResultFuncOutcomes(t *testing.T) {
	var mu sync.Mutex
	var got []FetchOutcome
	f, err := NewFetcher(Config{
		Workers:          1,
		Shards:           1,
		Timeout:          5 * time.Second,
		PrescreenEnabled: true,
		PrescreenTimeout: 100 * time.Millisecond,
		ResultFunc: func(hash string, o FetchOutcome) {
			mu.Lock()
			got = append(got, o)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if _, ok := f.fetch(context.Background(), "not-a-hash"); ok {
		t.Fatal("malformed hash fetched")
	}
	if _, ok := f.fetch(context.Background(), "97a29535b9e2f0e46a1b09a4b0c0e4f0a1b2c3d4"); ok {
		t.Fatal("peerless hash fetched")
	}

	mu.Lock()
	defer mu.Unlock()
	want := []FetchOutcome{OutcomeFailed, OutcomePrescreened}
	if len(got) != len(want) {
		t.Fatalf("outcomes = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("outcomes = %v, want %v", got, want)
		}
	}
	// The counters and the reported outcomes agree.
	if _, _, failed := f.Stats(); failed != 1 {
		t.Fatalf("failed counter = %d, want 1", failed)
	}
	if pd := f.PrescreenDropped(); pd != 1 {
		t.Fatalf("prescreen counter = %d, want 1", pd)
	}
}
