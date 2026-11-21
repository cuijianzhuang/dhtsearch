package main

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"dhtsearch/server/internal/nodepush"
	"dhtsearch/server/internal/scraper"
)

// TestRunsHousekeeping pins the worker-mode guard: retention and
// re-scrape run on standalone and center nodes (which hold the
// index) and never on a crawler worker (whose local index is empty
// by design).
func TestRunsHousekeeping(t *testing.T) {
	if !runsHousekeeping(false) {
		t.Fatal("standalone/center node must run housekeeping")
	}
	if runsHousekeeping(true) {
		t.Fatal("worker node must not run housekeeping")
	}
}

// gatedTestCenter answers node checks from a stored set.
func gatedTestCenter(t *testing.T, stored map[string]bool) *nodepush.Checker {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/node/check", func(w http.ResponseWriter, r *http.Request) {
		var req nodepush.CheckRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		resp := nodepush.CheckResponse{Stored: []string{}, Backoff: []string{}}
		for _, h := range req.Hashes {
			if stored[h] {
				resp.Stored = append(resp.Stored, h)
			}
		}
		_ = json.NewEncoder(w).Encode(resp)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client := nodepush.NewClient(srv.URL, "tok", "worker-1", nil)
	return nodepush.NewChecker(client, log.New(io.Discard, "", 0))
}

// TestGatedHashesDropsCenterStored covers the worker bridge for
// harvested hashes: a hash the center already stores never reaches
// the fetch queue; a fresh one passes through unchanged.
func TestGatedHashesDropsCenterStored(t *testing.T) {
	storedHash := strings.Repeat("a", 40)
	freshHash := strings.Repeat("b", 40)
	checker := gatedTestCenter(t, map[string]bool{storedHash: true})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := make(chan string, 2)
	in <- storedHash
	in <- freshHash
	close(in)
	out := gatedHashes(ctx, checker, in)
	var got []string
	timeout := time.After(5 * time.Second)
	for {
		select {
		case h, ok := <-out:
			if !ok {
				if len(got) != 1 || got[0] != freshHash {
					t.Fatalf("gated hashes = %v, want [%s]", got, freshHash)
				}
				return
			}
			got = append(got, h)
		case <-timeout:
			t.Fatal("gated hash stream did not drain")
		}
	}
}

// TestGatedProbesKeepsSeeders covers the scraper bridge: admitted
// probes keep the seeder count the ranking depends on.
func TestGatedProbesKeepsSeeders(t *testing.T) {
	storedHash := strings.Repeat("a", 40)
	freshHash := strings.Repeat("b", 40)
	checker := gatedTestCenter(t, map[string]bool{storedHash: true})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := make(chan scraper.Item, 2)
	in <- scraper.Item{Hash: storedHash, Seeders: 99}
	in <- scraper.Item{Hash: freshHash, Seeders: 7}
	close(in)
	out := gatedProbes(ctx, checker, in)
	timeout := time.After(5 * time.Second)
	select {
	case p, ok := <-out:
		if !ok {
			t.Fatal("gated probe stream closed before the fresh probe")
		}
		if p.Hash != freshHash || p.Seeders != 7 {
			t.Fatalf("gated probe = %+v, want fresh hash with 7 seeders", p)
		}
	case <-timeout:
		t.Fatal("gated probe stream did not deliver")
	}
	select {
	case p, ok := <-out:
		if ok {
			t.Fatalf("unexpected second probe %+v (the stored one must be gated out)", p)
		}
	case <-timeout:
		t.Fatal("gated probe stream did not close")
	}
}
