package metadata

import "testing"

// TestBackendStatKey: the mirror's key shape —
// fetch_backend_<backend>_<counter>, reusing the per-source
// counter names so both breakdowns read alike.
func TestBackendStatKey(t *testing.T) {
	if got := BackendStatKey(BackendNative, SourceCounterTimedOut); got != "fetch_backend_native_timed_out" {
		t.Fatalf("key = %q, want fetch_backend_native_timed_out", got)
	}
	if got := BackendStatKey(BackendLibtorrent, SourceCounterAttempts); got != "fetch_backend_libtorrent_attempts" {
		t.Fatalf("key = %q, want fetch_backend_libtorrent_attempts", got)
	}
	// An unknown name folds into anacrolix, the fallback
	// foundation its fetches would have run on.
	if got := BackendStatKey("bogus", SourceCounterFetched); got != "fetch_backend_anacrolix_fetched" {
		t.Fatalf("key = %q, want fetch_backend_anacrolix_fetched", got)
	}
}

// TestAllBackendsOrder: the canonical publication order the API
// rows follow.
func TestAllBackendsOrder(t *testing.T) {
	got := AllBackends()
	want := []string{"anacrolix", "native", "libtorrent"}
	if len(got) != len(want) {
		t.Fatalf("AllBackends = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("AllBackends = %v, want %v", got, want)
		}
	}
}

// TestBackendTotalsFoldsSources: the foundation row is the
// per-source rows summed — the two breakdowns reconcile — and the
// row carries the effective foundation name it was folded under.
func TestBackendTotalsFoldsSources(t *testing.T) {
	srcs := []FetchSourceStats{
		{Source: SourceDHT, Attempts: 10, Fetched: 3, TimedOut: 4, Failed: 1, PrescreenDropped: 2},
		{Source: SourceScrape, Attempts: 5, Fetched: 5},
		{Source: SourceFeed, Attempts: 2, Fetched: 1, Failed: 1},
	}
	got := BackendTotals(BackendNative, srcs)
	want := FetchBackendStats{
		Backend: "native", Attempts: 17, Fetched: 9, TimedOut: 4, Failed: 2, PrescreenDropped: 2,
	}
	if got != want {
		t.Fatalf("BackendTotals = %+v, want %+v", got, want)
	}
	// A degraded/unknown selection name folds to anacrolix: the
	// fetches are attributed to the foundation that executed them.
	if got := BackendTotals("libtorrent-typo", srcs); got.Backend != "anacrolix" {
		t.Fatalf("BackendTotals(bogus).Backend = %q, want anacrolix", got.Backend)
	}
	if got := BackendTotals(BackendLibtorrent, nil); got.Backend != "libtorrent" || got.Attempts != 0 {
		t.Fatalf("BackendTotals(empty) = %+v, want a zeroed libtorrent row", got)
	}
}
