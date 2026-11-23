package api

import (
	"net/http"
	"testing"
)

// TestFetchByBackendBreakdown: the per-foundation counters
// persisted in the stats table (fetch_backend_<backend>_<counter>,
// written by cmd/server's mirror) surface as the fetch section's
// by_backend rows in BOTH /api/stats and the admin overview, with
// the success rate derived at read time.
func TestFetchByBackendBreakdown(t *testing.T) {
	base, st := adminFixture(t, testAdminToken)
	seed := map[string]int64{
		"fetch_backend_anacrolix_attempts": 200, "fetch_backend_anacrolix_fetched": 50,
		"fetch_backend_anacrolix_timed_out": 120, "fetch_backend_anacrolix_failed": 10,
		"fetch_backend_anacrolix_prescreen_dropped": 20,
		"fetch_backend_native_attempts":             80, "fetch_backend_native_fetched": 60,
		"fetch_backend_native_prescreen_dropped": 20,
		// libtorrent never ran on this install: no keys at all.
	}
	for key, delta := range seed {
		if err := st.IncrStat(key, delta); err != nil {
			t.Fatal(err)
		}
	}

	assertRows := func(t *testing.T, payload map[string]any) {
		t.Helper()
		fetch, ok := payload["fetch"].(map[string]any)
		if !ok {
			t.Fatalf("no fetch section in %v", payload)
		}
		rows, ok := fetch["by_backend"].([]any)
		if !ok || len(rows) != 3 {
			t.Fatalf("by_backend = %v, want 3 rows", fetch["by_backend"])
		}
		want := []struct {
			backend           string
			attempts, fetched float64
			rate              float64
		}{
			{"anacrolix", 200, 50, 0.25},
			{"native", 80, 60, 0.75},
			{"libtorrent", 0, 0, 0},
		}
		for i, w := range want {
			row, ok := rows[i].(map[string]any)
			if !ok {
				t.Fatalf("row %d is %T", i, rows[i])
			}
			if row["backend"] != w.backend || row["attempts"] != w.attempts || row["fetched"] != w.fetched {
				t.Fatalf("row %d = %v, want backend %s attempts %v fetched %v", i, row, w.backend, w.attempts, w.fetched)
			}
			if row["success_rate"] != w.rate {
				t.Fatalf("row %d success_rate = %v, want %v", i, row["success_rate"], w.rate)
			}
		}
		ana := rows[0].(map[string]any)
		if ana["timed_out"] != 120.0 || ana["failed"] != 10.0 || ana["prescreen_dropped"] != 20.0 {
			t.Fatalf("anacrolix row outcomes = %v", ana)
		}
	}

	code, payload := adminReq(t, "GET", base+"/api/stats", "", nil)
	if code != http.StatusOK {
		t.Fatalf("stats: %d", code)
	}
	assertRows(t, payload)

	code, payload = adminReq(t, "GET", base+"/api/admin/overview", testAdminToken, nil)
	if code != http.StatusOK {
		t.Fatalf("overview: %d", code)
	}
	assertRows(t, payload)
}

// TestFetchByBackendEmpty: with no per-foundation counters
// persisted (a fresh install, or one upgraded from before the
// attribution existed), the breakdown still renders — three zero
// rows in canonical order with a zero rate.
func TestFetchByBackendEmpty(t *testing.T) {
	rows := fetchByBackend(map[string]int64{})
	if len(rows) != 3 {
		t.Fatalf("rows = %v, want 3", rows)
	}
	for _, row := range rows {
		if row.Attempts != 0 || row.SuccessRate != 0 {
			t.Fatalf("row = %+v, want zeroed", row)
		}
	}
	if rows[0].Backend != "anacrolix" || rows[1].Backend != "native" || rows[2].Backend != "libtorrent" {
		t.Fatalf("row order = %v", rows)
	}
}
