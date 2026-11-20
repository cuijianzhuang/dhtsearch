package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"dhtsearch/server/internal/filter"
	"dhtsearch/server/internal/store"
)

// swarmFixture builds a server over a store holding three torrents:
// one with live seeders, one scraped into likely_dead, one never
// scraped. It returns the base URL and the three hashes.
func swarmFixture(t *testing.T) (string, map[string]string) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	hashes := map[string]string{
		"alive":   strings.Repeat("a", 40),
		"dead":    strings.Repeat("d", 40),
		"unknown": strings.Repeat("e", 40),
	}
	for i, name := range []string{"alive", "dead", "unknown"} {
		if err := st.Upsert(store.Torrent{
			InfoHash:  hashes[name],
			Name:      "Swarm " + name + " Movie",
			TotalSize: 1 << 30,
			FileCount: 1,
			Files:     []filter.File{{Path: name + ".mkv", Size: 1 << 30}},
			CreatedAt: int64(100 * (i + 1)),
		}); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	if err := st.UpsertSwarmStats(ctx, []store.SwarmStats{
		{InfoHash: hashes["alive"], Seeders: 50, Leechers: 2, Completed: 900, ScrapedAt: 1000},
		{InfoHash: hashes["dead"], Seeders: 0, Leechers: 0, Completed: 3, ScrapedAt: 1000},
	}); err != nil {
		t.Fatal(err)
	}
	for _, at := range []int64{1000 + 24*3600, 1000 + 72*3600} {
		if err := st.UpsertSwarmStats(ctx, []store.SwarmStats{
			{InfoHash: hashes["dead"], Seeders: 0, Leechers: 0, Completed: 3, ScrapedAt: at},
		}); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(New(st, nil, nil, Options{AdminToken: testAdminToken}).Handler())
	t.Cleanup(srv.Close)
	return srv.URL, hashes
}

func resultByHash(t *testing.T, m map[string]any) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for _, r := range m["results"].([]any) {
		item := r.(map[string]any)
		out[item["info_hash"].(string)] = item
	}
	return out
}

// TestSearchSwarmFields pins the wire contract: counters are
// numbers when a snapshot exists — including a scraped true zero —
// and JSON null when no tracker has ever reported, with the health
// label derived to match.
func TestSearchSwarmFields(t *testing.T) {
	base, hashes := swarmFixture(t)
	byHash := resultByHash(t, getJSON(t, base+"/api/search?q=Swarm"))

	alive := byHash[hashes["alive"]]
	if alive["seeders"] != float64(50) || alive["leechers"] != float64(2) ||
		alive["completed"] != float64(900) || alive["scraped_at"] != float64(1000) {
		t.Fatalf("alive swarm fields: %v", alive)
	}
	if alive["health"] != "alive" {
		t.Fatalf("alive health = %v", alive["health"])
	}

	dead := byHash[hashes["dead"]]
	if dead["seeders"] == nil || dead["seeders"] != float64(0) {
		t.Fatalf("dead seeders = %v, want a real 0 (not null)", dead["seeders"])
	}
	if dead["health"] != "likely_dead" || dead["zero_count"] != float64(3) ||
		dead["zero_since"] != float64(1000) {
		t.Fatalf("dead health/streak: health=%v zero_count=%v zero_since=%v",
			dead["health"], dead["zero_count"], dead["zero_since"])
	}

	unknown := byHash[hashes["unknown"]]
	for _, k := range []string{"seeders", "leechers", "completed", "scraped_at", "zero_since", "zero_count"} {
		if v, present := unknown[k]; !present || v != nil {
			t.Fatalf("unknown %s = %v (present %v), want JSON null", k, v, present)
		}
	}
	if unknown["health"] != "unknown" {
		t.Fatalf("unknown health = %v", unknown["health"])
	}
}

// TestSearchSortSeedersAndHideDead covers the two search modes the
// swarm data unlocks, end to end over HTTP.
func TestSearchSortSeedersAndHideDead(t *testing.T) {
	base, hashes := swarmFixture(t)

	m := getJSON(t, base+"/api/search?q=Swarm&sort=seeders")
	results := m["results"].([]any)
	want := []string{hashes["alive"], hashes["dead"], hashes["unknown"]}
	if len(results) != 3 {
		t.Fatalf("sort=seeders returned %d results, want 3", len(results))
	}
	for i, w := range want {
		if got := results[i].(map[string]any)["info_hash"]; got != w {
			t.Fatalf("sort=seeders position %d = %v, want %s", i, got, w)
		}
	}

	m = getJSON(t, base+"/api/search?q=Swarm&hide_dead=1")
	if m["total"] != float64(2) || m["hide_dead"] != true {
		t.Fatalf("hide_dead: total=%v hide_dead=%v, want 2/true", m["total"], m["hide_dead"])
	}
	for _, r := range m["results"].([]any) {
		if r.(map[string]any)["info_hash"] == hashes["dead"] {
			t.Fatal("hide_dead kept the likely_dead torrent")
		}
	}

	resp, err := http.Get(base + "/api/search?q=Swarm&hide_dead=banana")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("hide_dead=banana: status %d, want 400", resp.StatusCode)
	}
}

// TestAdminDetailSwarmFields: the admin detail carries the same
// swarm shape as the public search.
func TestAdminDetailSwarmFields(t *testing.T) {
	base, hashes := swarmFixture(t)

	code, m := adminReq(t, "GET", base+"/api/admin/torrents/"+hashes["dead"], testAdminToken, nil)
	if code != 200 {
		t.Fatalf("admin detail: %d", code)
	}
	if m["seeders"] != float64(0) || m["health"] != "likely_dead" || m["zero_count"] != float64(3) {
		t.Fatalf("admin dead swarm: seeders=%v health=%v zero_count=%v",
			m["seeders"], m["health"], m["zero_count"])
	}

	code, m = adminReq(t, "GET", base+"/api/admin/torrents/"+hashes["unknown"], testAdminToken, nil)
	if code != 200 {
		t.Fatalf("admin detail unknown: %d", code)
	}
	if m["seeders"] != nil || m["health"] != "unknown" {
		t.Fatalf("admin unknown swarm: seeders=%v health=%v, want null/unknown",
			m["seeders"], m["health"])
	}
}
