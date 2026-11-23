package main

import (
	"context"
	"io"
	"log"
	"path/filepath"
	"testing"

	"dhtsearch/server/internal/metadata"
	"dhtsearch/server/internal/store"
)

// TestBackendStatMirrorRoundTrip: the main foundation's counters,
// folded over sources, land in the stats table under the
// per-foundation keys when applied through the mirror; a second
// application moves only the deltas, and a third (unchanged
// counters) moves nothing — the persistence the by_backend
// breakdown reads back after a restart.
func TestBackendStatMirrorRoundTrip(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "backend-mirror.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	m := newBackendStatMirror()

	totals := metadata.BackendTotals("native", []metadata.FetchSourceStats{
		{Source: metadata.SourceDHT, Attempts: 10, Fetched: 3, TimedOut: 4, Failed: 1, PrescreenDropped: 2},
		{Source: metadata.SourceScrape, Attempts: 5, Fetched: 5},
	})
	m.apply(ctx, st, totals)
	got, err := st.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{
		"fetch_backend_native_attempts": 15, "fetch_backend_native_fetched": 8,
		"fetch_backend_native_timed_out": 4, "fetch_backend_native_failed": 1,
		"fetch_backend_native_prescreen_dropped": 2,
	}
	for key, w := range want {
		if got[key] != w {
			t.Fatalf("stats[%q] = %d, want %d (all: %v)", key, got[key], w, got)
		}
	}
	// Nothing leaked under another foundation's keys.
	for key := range got {
		if len(key) > 14 && key[:14] == "fetch_backend_" && got[key] != 0 {
			if _, ok := want[key]; !ok {
				t.Fatalf("unexpected key %q = %d", key, got[key])
			}
		}
	}

	// Growth applies as deltas only.
	totals.Attempts, totals.Fetched = 21, 11
	m.apply(ctx, st, totals)
	got, err = st.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got["fetch_backend_native_attempts"] != 21 || got["fetch_backend_native_fetched"] != 11 {
		t.Fatalf("after growth: attempts=%d fetched=%d, want 21/11",
			got["fetch_backend_native_attempts"], got["fetch_backend_native_fetched"])
	}
	// Unchanged application moves nothing.
	m.apply(ctx, st, totals)
	got, err = st.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got["fetch_backend_native_attempts"] != 21 {
		t.Fatalf("after unchanged apply: attempts=%d, want 21", got["fetch_backend_native_attempts"])
	}
}

// TestBackendMirrorAttributesDegradedSelection is the attribution
// half of the 2026-11-20 degradation: META_FETCHER=libtorrent
// with a missing sidecar runs the anacrolix fallback, and the
// fetches that fallback executes must land under the anacrolix
// keys — never under libtorrent, which executed nothing. The
// mirror keys off selectBackend's effective name, so this drives
// the real selection into the real mirror.
func TestBackendMirrorAttributesDegradedSelection(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-sidecar")
	sel, err := selectBackend("libtorrent", missing, selectTestConfig(), log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("selectBackend: %v", err)
	}
	defer sel.backend.Close()
	if sel.name != "anacrolix" {
		t.Fatalf("effective backend = %q, want anacrolix fallback", sel.name)
	}

	st, err := store.Open(filepath.Join(t.TempDir(), "degraded-mirror.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	m := newBackendStatMirror()
	m.apply(ctx, st, metadata.BackendTotals(sel.name, []metadata.FetchSourceStats{
		{Source: metadata.SourceDHT, Attempts: 7, Fetched: 2, TimedOut: 5},
	}))
	got, err := st.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got["fetch_backend_anacrolix_attempts"] != 7 || got["fetch_backend_anacrolix_fetched"] != 2 {
		t.Fatalf("anacrolix keys = attempts %d fetched %d, want 7/2 (all: %v)",
			got["fetch_backend_anacrolix_attempts"], got["fetch_backend_anacrolix_fetched"], got)
	}
	if _, ok := got["fetch_backend_libtorrent_attempts"]; ok {
		t.Fatalf("libtorrent keys present (%v) though libtorrent executed nothing", got)
	}
}
