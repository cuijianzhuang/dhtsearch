package api

import (
	"net/http"
	"testing"
)

// TestFetchBySourceBreakdown: the per-source counters persisted in
// the stats table (fetch_<source>_<counter>, written by cmd/server's
// mirror) surface as the fetch section's by_source rows in BOTH
// /api/stats and the admin overview, with the success rate derived
// at read time.
func TestFetchBySourceBreakdown(t *testing.T) {
	base, st := adminFixture(t, testAdminToken)
	seed := map[string]int64{
		"fetch_dht_attempts": 100, "fetch_dht_fetched": 25,
		"fetch_dht_timed_out": 60, "fetch_dht_failed": 5, "fetch_dht_prescreen_dropped": 10,
		"fetch_scrape_attempts": 40, "fetch_scrape_fetched": 30, "fetch_scrape_prescreen_dropped": 10,
		"fetch_feed_attempts": 3, "fetch_feed_fetched": 1, "fetch_feed_failed": 2,
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
		rows, ok := fetch["by_source"].([]any)
		if !ok || len(rows) != 3 {
			t.Fatalf("by_source = %v, want 3 rows", fetch["by_source"])
		}
		want := []struct {
			source            string
			attempts, fetched float64
			rate              float64
		}{
			{"dht", 100, 25, 0.25},
			{"scrape", 40, 30, 0.75},
			{"feed", 3, 1, 0.3333},
		}
		for i, w := range want {
			row, ok := rows[i].(map[string]any)
			if !ok {
				t.Fatalf("row %d is %T", i, rows[i])
			}
			if row["source"] != w.source || row["attempts"] != w.attempts || row["fetched"] != w.fetched {
				t.Fatalf("row %d = %v, want source %s attempts %v fetched %v", i, row, w.source, w.attempts, w.fetched)
			}
			if row["success_rate"] != w.rate {
				t.Fatalf("row %d success_rate = %v, want %v", i, row["success_rate"], w.rate)
			}
		}
		dht := rows[0].(map[string]any)
		if dht["timed_out"] != 60.0 || dht["failed"] != 5.0 || dht["prescreen_dropped"] != 10.0 {
			t.Fatalf("dht row outcomes = %v", dht)
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

// TestFetchBySourceEmpty: with no per-source counters persisted
// (a fresh install, or one upgraded from before the attribution
// existed), the breakdown still renders — three zero rows with a
// zero rate, never a missing section or a NaN.
func TestFetchBySourceEmpty(t *testing.T) {
	rows := fetchBySource(map[string]int64{})
	if len(rows) != 3 {
		t.Fatalf("rows = %v, want 3", rows)
	}
	for _, row := range rows {
		if row.Attempts != 0 || row.SuccessRate != 0 {
			t.Fatalf("row = %+v, want zeroed", row)
		}
	}
	if rows[0].Source != "dht" || rows[1].Source != "scrape" || rows[2].Source != "feed" {
		t.Fatalf("row order = %v", rows)
	}
}
