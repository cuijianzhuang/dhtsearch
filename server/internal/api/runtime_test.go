package api

import (
	"testing"
)

// TestRuntimeStatusSane: the gauges are live process readings, so
// the heap figures must be positive and self-consistent, and the
// goroutine count must at least cover the test's own goroutines.
// RSS is platform-dependent (0 off Linux) and only sanity-checked
// when present.
func TestRuntimeStatusSane(t *testing.T) {
	rs := runtimeStatus()
	if rs.HeapAlloc == 0 || rs.HeapSys < rs.HeapAlloc {
		t.Fatalf("heap figures inconsistent: %+v", rs)
	}
	if rs.Goroutines < 1 {
		t.Fatalf("goroutines = %d, want >= 1", rs.Goroutines)
	}
	// RSS is only required to be a plausible process size when
	// reported: it can legitimately sit below HeapAlloc (heap pages
	// the runtime has obtained but not touched are not resident),
	// so no ordering between the two is asserted.
	if rs.RSS != 0 && rs.RSS < 1<<20 {
		t.Fatalf("rss = %d, implausibly small for a live process", rs.RSS)
	}
}

// TestStatsCarriesRuntime: /api/stats publishes the runtime section
// next to the pipeline counters.
func TestStatsCarriesRuntime(t *testing.T) {
	m := getJSON(t, testServer(t).URL+"/api/stats")
	rt, ok := m["runtime"].(map[string]any)
	if !ok {
		t.Fatalf("stats has no runtime section: %v", m)
	}
	for _, k := range []string{"heap_alloc", "heap_sys", "rss", "goroutines"} {
		if _, ok := rt[k]; !ok {
			t.Fatalf("runtime section missing %q: %v", k, rt)
		}
	}
	if rt["heap_alloc"].(float64) <= 0 {
		t.Fatalf("heap_alloc = %v, want > 0", rt["heap_alloc"])
	}
}
