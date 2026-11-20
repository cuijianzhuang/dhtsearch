package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"dhtsearch/server/internal/filter"
	"dhtsearch/server/internal/store"
)

const testAdminToken = "test-admin-token"

// adminFixture seeds three torrents and serves the API with the given
// admin token ("" = admin disabled).
func adminFixture(t *testing.T, token string) (string, *store.Store) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for i, name := range []string{"Big Buck Bunny", "Sintel", "Ubuntu 24.04"} {
		if err := st.Upsert(store.Torrent{
			InfoHash:  strings.Repeat(string(rune('a'+i)), 40),
			Name:      name,
			TotalSize: int64(1+i) << 30,
			FileCount: 1,
			Files:     []filter.File{{Path: name + ".mkv", Size: int64(1+i) << 30}},
			CreatedAt: int64(100 + i),
		}); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(New(st, nil, nil, Options{AdminToken: token}).Handler())
	t.Cleanup(srv.Close)
	return srv.URL, st
}

func adminReq(t *testing.T, method, url, token string, body any) (int, map[string]any) {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	return resp.StatusCode, m
}

func TestAdminDisabledWithoutToken(t *testing.T) {
	base, _ := adminFixture(t, "")
	if code, _ := adminReq(t, "GET", base+"/api/admin/overview", "", nil); code != http.StatusServiceUnavailable {
		t.Fatalf("overview with admin disabled: %d", code)
	}
	if code, _ := adminReq(t, "POST", base+"/api/admin/login", "", map[string]string{"token": "x"}); code != http.StatusServiceUnavailable {
		t.Fatalf("login with admin disabled: %d", code)
	}
	// Public API unaffected.
	if code, _ := adminReq(t, "GET", base+"/api/healthz", "", nil); code != 200 {
		t.Fatalf("healthz: %d", code)
	}
}

// TestFetchBackendSurfaced: the effective fetch foundation and its
// degradation warning ride the fetch section of both /api/stats and
// the admin overview, so a startup degradation (libtorrent selected,
// anacrolix running) is visible to the admin instead of silent.
func TestFetchBackendSurfaced(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	const warning = "META_FETCHER=libtorrent requested but the sidecar is unavailable; running on the anacrolix fallback"
	srv := httptest.NewServer(New(st, nil, nil, Options{
		AdminToken: testAdminToken,
		MetaStatus: func() MetaStatus {
			return MetaStatus{QueueDepth: 7, Backend: "anacrolix", BackendWarning: warning,
				DHT: &BackendDHT{Running: true, Nodes: 321}}
		},
	}).Handler())
	t.Cleanup(srv.Close)

	for _, path := range []string{"/api/stats", "/api/admin/overview"} {
		code, m := adminReq(t, "GET", srv.URL+path, testAdminToken, nil)
		if code != 200 {
			t.Fatalf("%s: %d %v", path, code, m)
		}
		fetch, _ := m["fetch"].(map[string]any)
		if fetch["backend"] != "anacrolix" || fetch["backend_warning"] != warning {
			t.Fatalf("%s fetch = %v, want backend + warning surfaced", path, fetch)
		}
		if fetch["queue_depth"].(float64) != 7 {
			t.Fatalf("%s queue_depth = %v", path, fetch["queue_depth"])
		}
		dht, _ := fetch["dht"].(map[string]any)
		if dht["running"] != true || dht["nodes"].(float64) != 321 {
			t.Fatalf("%s dht = %v, want running + 321 nodes surfaced", path, fetch["dht"])
		}
	}

	// Without a MetaStatus provider the keys stay absent (the
	// historical shape), not empty strings.
	base, _ := adminFixture(t, testAdminToken)
	_, m := adminReq(t, "GET", base+"/api/stats", "", nil)
	fetch, _ := m["fetch"].(map[string]any)
	if _, ok := fetch["backend"]; ok {
		t.Fatalf("backend key present without MetaStatus: %v", fetch)
	}
	if _, ok := fetch["queue_depth"]; ok {
		t.Fatalf("queue_depth key present without MetaStatus: %v", fetch)
	}
	if _, ok := fetch["dht"]; ok {
		t.Fatalf("dht key present without MetaStatus: %v", fetch)
	}
}

func TestAdminAuthRequired(t *testing.T) {
	base, _ := adminFixture(t, testAdminToken)
	if code, _ := adminReq(t, "GET", base+"/api/admin/overview", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", code)
	}
	if code, _ := adminReq(t, "GET", base+"/api/admin/overview", "wrong", nil); code != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d", code)
	}
	if code, m := adminReq(t, "GET", base+"/api/admin/overview", testAdminToken, nil); code != 200 {
		t.Fatalf("good token: %d %v", code, m)
	} else if m["torrents"].(float64) != 3 || m["pending_review"].(float64) != 3 || m["blocked"].(float64) != 0 {
		t.Fatalf("overview numbers: %v", m)
	}
}

// The pipeline hero's 入库 stage reads stored_total: the library's
// actual row count, not a derivation from the cumulative counters.
// A re-fetch of an already-known hash upserts the same row again —
// main.go's onRecord increments "fetched" for every successful
// upsert — so after 4 upserts over 3 distinct hashes the fetched
// counter says 4 while stored_total must still say 3.
func TestAdminOverviewStoredTotal(t *testing.T) {
	base, st := adminFixture(t, testAdminToken)
	if err := st.Upsert(store.Torrent{
		InfoHash:  strings.Repeat("a", 40), // same hash as a seeded torrent
		Name:      "Big Buck Bunny",
		TotalSize: 1 << 30,
		FileCount: 1,
		Files:     []filter.File{{Path: "Big Buck Bunny.mkv", Size: 1 << 30}},
		CreatedAt: 100,
	}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if err := st.IncrStat("fetched", 1); err != nil {
			t.Fatal(err)
		}
	}
	code, m := adminReq(t, "GET", base+"/api/admin/overview", testAdminToken, nil)
	if code != 200 {
		t.Fatalf("overview: %d %v", code, m)
	}
	if got := m["stored_total"].(float64); got != 3 {
		t.Fatalf("stored_total = %v, want 3 (distinct torrents)", got)
	}
	if got := m["torrents"].(float64); got != 3 {
		t.Fatalf("torrents = %v, want 3", got)
	}
	if got := m["stats"].(map[string]any)["fetched"].(float64); got != 4 {
		t.Fatalf("stats.fetched = %v, want 4 (every upsert counts)", got)
	}
}

func TestAdminLogin(t *testing.T) {
	base, _ := adminFixture(t, testAdminToken)
	if code, _ := adminReq(t, "POST", base+"/api/admin/login", "", map[string]string{"token": "nope"}); code != http.StatusUnauthorized {
		t.Fatalf("bad login: %d", code)
	}
	if code, _ := adminReq(t, "POST", base+"/api/admin/login", "", map[string]string{"token": testAdminToken}); code != 200 {
		t.Fatalf("good login: %d", code)
	}
}

// The documented login-limit semantics, pinned: the per-client token
// bucket starts full at adminLoginBurst, so exactly that many attempts
// pass the limiter back-to-back (bad ones answer 401) and the next one
// is a 429; the bucket then refills one token per 1/adminLoginRate
// seconds. README's wording states this burst-then-refill shape — if
// the constants are retuned, this test and the README move together.
func TestAdminLoginRateLimitBurstSemantics(t *testing.T) {
	base, _ := adminFixture(t, testAdminToken)
	for i := 1; i <= adminLoginBurst; i++ {
		code, _ := adminReq(t, "POST", base+"/api/admin/login", "", map[string]string{"token": "nope"})
		if code != http.StatusUnauthorized {
			t.Fatalf("attempt %d of %d: %d, want 401 (limiter must let the burst through)", i, adminLoginBurst, code)
		}
	}
	code, _ := adminReq(t, "POST", base+"/api/admin/login", "", map[string]string{"token": "nope"})
	if code != http.StatusTooManyRequests {
		t.Fatalf("attempt %d: %d, want 429 (bucket empty)", adminLoginBurst+1, code)
	}
}

// Login attempts are throttled well below the general API rate: a burst
// of bad guesses must start drawing 429s.
func TestAdminLoginRateLimited(t *testing.T) {
	base, _ := adminFixture(t, testAdminToken)
	got429 := false
	for i := 0; i < 10; i++ {
		code, _ := adminReq(t, "POST", base+"/api/admin/login", "", map[string]string{"token": "nope"})
		if code == http.StatusTooManyRequests {
			got429 = true
			break
		}
		if code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i, code)
		}
	}
	if !got429 {
		t.Fatal("login attempts were never rate limited")
	}
}

func TestAdminListAndReviewFlow(t *testing.T) {
	base, _ := adminFixture(t, testAdminToken)
	hashA := strings.Repeat("a", 40)

	code, m := adminReq(t, "GET", base+"/api/admin/torrents?page_size=2&page=1", testAdminToken, nil)
	if code != 200 || m["total"].(float64) != 3 || len(m["results"].([]any)) != 2 {
		t.Fatalf("list p1: %d %v", code, m)
	}
	code, m = adminReq(t, "GET", base+"/api/admin/torrents?page_size=2&page=2", testAdminToken, nil)
	if code != 200 || len(m["results"].([]any)) != 1 {
		t.Fatalf("list p2: %d %v", code, m)
	}
	// Keyword filter.
	_, m = adminReq(t, "GET", base+"/api/admin/torrents?q=sintel", testAdminToken, nil)
	if m["total"].(float64) != 1 {
		t.Fatalf("keyword list: %v", m)
	}
	// Detail carries metadata only; the file list is paged separately
	// through the files endpoint (see files_test.go for the full
	// paging contract).
	code, m = adminReq(t, "GET", base+"/api/admin/torrents/"+hashA, testAdminToken, nil)
	if code != 200 {
		t.Fatalf("detail: %d %v", code, m)
	}
	if _, ok := m["files"]; ok {
		t.Fatalf("detail still embeds the file list: %v", m)
	}
	code, m = adminReq(t, "GET", base+"/api/admin/torrents/"+hashA+"/files", testAdminToken, nil)
	if code != 200 || len(m["files"].([]any)) != 1 {
		t.Fatalf("detail files: %d %v", code, m)
	}
	if code, _ := adminReq(t, "GET", base+"/api/admin/torrents/"+strings.Repeat("z", 40), testAdminToken, nil); code != http.StatusNotFound {
		t.Fatalf("missing detail: %d", code)
	}
	// Invalid status is a 400, not a silent all.
	if code, _ := adminReq(t, "GET", base+"/api/admin/torrents?status=bogus", testAdminToken, nil); code != http.StatusBadRequest {
		t.Fatalf("bad status: %d", code)
	}

	// Approve one, then the status filters split 1/2.
	if code, _ := adminReq(t, "POST", base+"/api/admin/torrents/"+hashA+"/approve", testAdminToken, nil); code != 200 {
		t.Fatalf("approve: %d", code)
	}
	_, m = adminReq(t, "GET", base+"/api/admin/torrents?status=unreviewed", testAdminToken, nil)
	if m["total"].(float64) != 2 {
		t.Fatalf("unreviewed after approve: %v", m)
	}
	_, m = adminReq(t, "GET", base+"/api/admin/torrents?status=reviewed", testAdminToken, nil)
	if m["total"].(float64) != 1 {
		t.Fatalf("reviewed after approve: %v", m)
	}
}

func TestAdminBlockUnblockAndDelete(t *testing.T) {
	base, st := adminFixture(t, testAdminToken)
	hashB := strings.Repeat("b", 40)
	hashC := strings.Repeat("c", 40)

	// Block removes the torrent and records it; re-adding is refused.
	if code, _ := adminReq(t, "POST", base+"/api/admin/torrents/"+hashB+"/block", testAdminToken, map[string]string{"reason": "spam"}); code != 200 {
		t.Fatalf("block: %d", code)
	}
	if blocked, _ := st.IsBlocked(hashB); !blocked {
		t.Fatal("hash not in blocked table after block")
	}
	_, m := adminReq(t, "GET", base+"/api/admin/torrents", testAdminToken, nil)
	if m["total"].(float64) != 2 {
		t.Fatalf("list after block: %v", m)
	}
	_, m = adminReq(t, "GET", base+"/api/admin/blocked", testAdminToken, nil)
	if m["total"].(float64) != 1 || m["results"].([]any)[0].(map[string]any)["reason"] != "spam" {
		t.Fatalf("blocked list: %v", m)
	}
	// Unblock lifts the rejection.
	if code, _ := adminReq(t, "DELETE", base+"/api/admin/blocked/"+hashB, testAdminToken, nil); code != 200 {
		t.Fatalf("unblock: %d", code)
	}
	if blocked, _ := st.IsBlocked(hashB); blocked {
		t.Fatal("still blocked after unblock")
	}
	if code, _ := adminReq(t, "DELETE", base+"/api/admin/blocked/"+hashB, testAdminToken, nil); code != http.StatusNotFound {
		t.Fatalf("second unblock: %d", code)
	}

	// Delete removes without blocking.
	if code, _ := adminReq(t, "DELETE", base+"/api/admin/torrents/"+hashC, testAdminToken, nil); code != 200 {
		t.Fatalf("delete: %d", code)
	}
	if blocked, _ := st.IsBlocked(hashC); blocked {
		t.Fatal("delete must not block")
	}
	if n, _ := st.Count(t.Context()); n != 1 {
		t.Fatalf("count after delete: %d", n)
	}
	if code, _ := adminReq(t, "DELETE", base+"/api/admin/torrents/"+hashC, testAdminToken, nil); code != http.StatusNotFound {
		t.Fatalf("second delete: %d", code)
	}
}

func TestAdminBatch(t *testing.T) {
	base, _ := adminFixture(t, testAdminToken)
	hashes := []string{strings.Repeat("a", 40), strings.Repeat("b", 40)}

	code, m := adminReq(t, "POST", base+"/api/admin/torrents/batch", testAdminToken,
		map[string]any{"hashes": hashes, "action": "approve"})
	if code != 200 || m["affected"].(float64) != 2 {
		t.Fatalf("batch approve: %d %v", code, m)
	}
	_, m = adminReq(t, "GET", base+"/api/admin/overview", testAdminToken, nil)
	if m["pending_review"].(float64) != 1 {
		t.Fatalf("pending after batch approve: %v", m)
	}

	code, m = adminReq(t, "POST", base+"/api/admin/torrents/batch", testAdminToken,
		map[string]any{"hashes": hashes, "action": "block"})
	if code != 200 || m["affected"].(float64) != 2 {
		t.Fatalf("batch block: %d %v", code, m)
	}
	_, m = adminReq(t, "GET", base+"/api/admin/overview", testAdminToken, nil)
	if m["blocked"].(float64) != 2 || m["torrents"].(float64) != 1 {
		t.Fatalf("overview after batch block: %v", m)
	}

	// Batch delete removes the one torrent the block left behind,
	// without adding it to the blocked table.
	code, m = adminReq(t, "POST", base+"/api/admin/torrents/batch", testAdminToken,
		map[string]any{"hashes": []string{strings.Repeat("c", 40)}, "action": "delete"})
	if code != 200 || m["affected"].(float64) != 1 {
		t.Fatalf("batch delete: %d %v", code, m)
	}
	_, m = adminReq(t, "GET", base+"/api/admin/overview", testAdminToken, nil)
	if m["torrents"].(float64) != 0 {
		t.Fatalf("overview after batch delete: %v", m)
	}

	if code, _ := adminReq(t, "POST", base+"/api/admin/torrents/batch", testAdminToken,
		map[string]any{"hashes": hashes, "action": "explode"}); code != http.StatusBadRequest {
		t.Fatalf("bad batch action: %d", code)
	}
	// Batch without auth is refused.
	if code, _ := adminReq(t, "POST", base+"/api/admin/torrents/batch", "",
		map[string]any{"hashes": hashes, "action": "approve"}); code != http.StatusUnauthorized {
		t.Fatalf("batch without token: %d", code)
	}
}
func TestAdminBatchApproveCountsOnlyExisting(t *testing.T) {
	base, _ := adminFixture(t, testAdminToken)
	ghosts := []string{strings.Repeat("d", 40), strings.Repeat("e", 40), strings.Repeat("f", 40)}

	// Approving hashes that are not in the table must report 0, not
	// len(hashes) — the count used to be fabricated from the request.
	code, m := adminReq(t, "POST", base+"/api/admin/torrents/batch", testAdminToken,
		map[string]any{"hashes": ghosts, "action": "approve"})
	if code != 200 || m["affected"].(float64) != 0 {
		t.Fatalf("batch approve ghosts: %d %v, want affected 0", code, m)
	}

	// Mixed input reports only the rows actually stamped.
	mixed := append([]string{strings.Repeat("a", 40)}, ghosts...)
	code, m = adminReq(t, "POST", base+"/api/admin/torrents/batch", testAdminToken,
		map[string]any{"hashes": mixed, "action": "approve"})
	if code != 200 || m["affected"].(float64) != 1 {
		t.Fatalf("batch approve mixed: %d %v, want affected 1", code, m)
	}
}

func TestAdminTrends(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.Now().Unix()
	// Two in the current hour, one two hours ago, one yesterday
	// (14d only), one forty days ago (neither window).
	for i, ts := range []int64{now, now - 600, now - 2*3600, now - 26*3600, now - 40*86400} {
		if err := st.Upsert(store.Torrent{
			InfoHash:  strings.Repeat(string(rune('a'+i)), 40),
			Name:      "Trend Case",
			TotalSize: 1 << 30,
			FileCount: 1,
			Files:     []filter.File{{Path: "t.mkv", Size: 1 << 30}},
			CreatedAt: ts,
		}); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(New(st, nil, nil, Options{AdminToken: testAdminToken}).Handler())
	t.Cleanup(srv.Close)
	base := srv.URL

	if code, _ := adminReq(t, "GET", base+"/api/admin/trends", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("trends without token: %d", code)
	}
	if code, _ := adminReq(t, "GET", base+"/api/admin/trends?range=year", testAdminToken, nil); code != http.StatusBadRequest {
		t.Fatalf("trends bad range: %d", code)
	}

	sum := func(m map[string]any) float64 {
		var total float64
		for _, b := range m["buckets"].([]any) {
			total += b.(map[string]any)["count"].(float64)
		}
		return total
	}
	code, m := adminReq(t, "GET", base+"/api/admin/trends?range=24h", testAdminToken, nil)
	if code != 200 || len(m["buckets"].([]any)) != 24 || sum(m) != 3 {
		t.Fatalf("trends 24h: code=%d buckets=%v", code, m)
	}
	if m["bucket_seconds"].(float64) != 3600 {
		t.Fatalf("trends 24h bucket_seconds: %v", m["bucket_seconds"])
	}
	code, m = adminReq(t, "GET", base+"/api/admin/trends?range=14d", testAdminToken, nil)
	if code != 200 || len(m["buckets"].([]any)) != 14 || sum(m) != 4 {
		t.Fatalf("trends 14d: code=%d buckets=%v", code, m)
	}
	// Default range is 24h.
	code, m = adminReq(t, "GET", base+"/api/admin/trends", testAdminToken, nil)
	if code != 200 || m["range"] != "24h" {
		t.Fatalf("trends default: code=%d range=%v", code, m["range"])
	}
}

func TestAdminOverviewModerationEnabled(t *testing.T) {
	// No settings registry: the pass cannot be configured -> false.
	base, _ := adminFixture(t, testAdminToken)
	_, m := adminReq(t, "GET", base+"/api/admin/overview", testAdminToken, nil)
	if got := m["moderation"].(map[string]any)["enabled"]; got != false {
		t.Fatalf("moderation.enabled without settings = %v, want false", got)
	}
	// Registry with an API key and the (default-on) feature switch.
	base, _, _ = settingsFixture(t)
	_, m = adminReq(t, "GET", base+"/api/admin/overview", testAdminToken, nil)
	if got := m["moderation"].(map[string]any)["enabled"]; got != true {
		t.Fatalf("moderation.enabled with key = %v, want true", got)
	}
}
