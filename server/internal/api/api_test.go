package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"dhtsearch/server/internal/filter"
	"dhtsearch/server/internal/store"
)

func testServer(t *testing.T) *httptest.Server {
	return testServerOpts(t, Options{})
}

func testServerOpts(t *testing.T, opts Options) *httptest.Server {
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
	srv := httptest.NewServer(New(st, func() CrawlerStatus {
		return CrawlerStatus{Enabled: false, Seen: 42}
	}, nil, opts).Handler())
	t.Cleanup(srv.Close)
	return srv
}

func getJSON(t *testing.T, url string) map[string]any {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("%s: status %d", url, resp.StatusCode)
	}
	if resp.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("%s: missing CORS header", url)
	}
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestHealthz(t *testing.T) {
	m := getJSON(t, testServer(t).URL+"/api/healthz")
	if m["ok"] != true {
		t.Fatalf("healthz: %v", m)
	}
}

func TestSearch(t *testing.T) {
	base := testServer(t).URL

	m := getJSON(t, base+"/api/search?q=")
	if m["total"].(float64) != 3 {
		t.Fatalf("empty query total: %v", m)
	}
	results := m["results"].([]any)
	first := results[0].(map[string]any)
	if first["name"] != "Ubuntu 24.04" {
		t.Fatalf("expected newest first: %v", first["name"])
	}
	magnet := first["magnet"].(string)
	if !strings.HasPrefix(magnet, "magnet:?xt=urn:btih:") || !strings.Contains(magnet, "&dn=") {
		t.Fatalf("bad magnet: %s", magnet)
	}

	m = getJSON(t, base+"/api/search?q=big+bunny")
	if m["total"].(float64) != 1 {
		t.Fatalf("keyword search: %v", m["total"])
	}

	m = getJSON(t, base+"/api/search?q=&page_size=1&page=2")
	if len(m["results"].([]any)) != 1 || m["page"].(float64) != 2 {
		t.Fatalf("pagination: %v", m)
	}
}

// A huge page number must not translate into a huge OFFSET scan.
func TestSearchClampsDeepPagination(t *testing.T) {
	base := testServer(t).URL
	m := getJSON(t, base+"/api/search?q=&page=99999999&page_size=100")
	if got, want := m["page"].(float64), float64(maxOffset/100+1); got != want {
		t.Fatalf("page = %v, want clamped to %v", got, want)
	}
}

func TestStats(t *testing.T) {
	m := getJSON(t, testServer(t).URL+"/api/stats")
	if m["torrents"].(float64) != 3 {
		t.Fatalf("stats torrents: %v", m)
	}
	crawler := m["crawler"].(map[string]any)
	if crawler["seen_infohashes"].(float64) != 42 {
		t.Fatalf("crawler stats: %v", crawler)
	}
}

func TestOptionsPreflight(t *testing.T) {
	srv := testServer(t)
	req, _ := http.NewRequest(http.MethodOptions, srv.URL+"/api/search", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("OPTIONS: status %d", resp.StatusCode)
	}
}

// A page number large enough to overflow int64 when multiplied by page_size
// must still clamp. Written the other way round — checking the product against
// maxOffset — the multiplication wraps negative and slips straight past.
func TestSearchClampsOverflowingPage(t *testing.T) {
	base := testServer(t).URL
	for _, page := range []string{"9223372036854775807", "1000000000000000000", "92233720368547758"} {
		m := getJSON(t, base+"/api/search?q=&page_size=100&page="+page)
		if got, want := m["page"].(float64), float64(maxOffset/100+1); got != want {
			t.Errorf("page=%s: clamped to %v, want %v", page, got, want)
		}
	}
}

// The UI renders at most maxListedFiles entries and summarises the rest
// against file_count, so the response must not carry the tail.
func TestSearchTrimsFileList(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	const stored = 40
	files := make([]filter.File, stored)
	for i := range files {
		files[i] = filter.File{Path: fmt.Sprintf("Show/ep%02d.mkv", i), Size: 1 << 20}
	}
	if err := st.Upsert(store.Torrent{
		InfoHash: strings.Repeat("a", 40), Name: "Show Complete Series",
		TotalSize: 1 << 30, FileCount: stored, Files: files, CreatedAt: 100,
	}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(st, nil, nil, Options{}).Handler())
	t.Cleanup(srv.Close)

	m := getJSON(t, srv.URL+"/api/search?q=Show")
	first := m["results"].([]any)[0].(map[string]any)
	if got := len(first["files"].([]any)); got != maxListedFiles {
		t.Errorf("files in response = %d, want %d", got, maxListedFiles)
	}
	// The true count still travels, so the UI can say what it is hiding.
	if got := first["file_count"].(float64); got != stored {
		t.Errorf("file_count = %v, want %d", got, stored)
	}
}

// total_capped tells the UI that total is a floor rather than a count.
func TestSearchReportsUncappedTotal(t *testing.T) {
	m := getJSON(t, testServer(t).URL+"/api/search?q=Bunny")
	if m["total_capped"] != false {
		t.Errorf("total_capped = %v, want false for a small result set", m["total_capped"])
	}
}

// The frontend words its "we filter adult content" copy from this field, so
// it has to report the live setting rather than a default.
func TestStatsReportsAdultFilterSetting(t *testing.T) {
	for _, on := range []bool{true, false} {
		m := getJSON(t, testServerOpts(t, Options{FilterAdult: func() bool { return on }}).URL+"/api/stats")
		if m["filter_adult"] != on {
			t.Errorf("FilterAdult=%v: stats reported %v", on, m["filter_adult"])
		}
	}
	// A nil hook must read as "not filtering": that only ever hides the claim,
	// it can never make the UI promise filtering that is not happening.
	m := getJSON(t, testServer(t).URL+"/api/stats")
	if m["filter_adult"] != false {
		t.Errorf("zero value reported %v, want false", m["filter_adult"])
	}
	if _, ok := m["adult_indexed"]; !ok {
		t.Error("adult_indexed missing from stats")
	}
}

// The stats body carries live configuration, so a config change has to
// invalidate it. Otherwise the setting stays invisible for a full TTL — and
// the frontend caches that stale answer for minutes, leaving the site
// advertising filtering it had just been told to stop.
func TestStatsCacheInvalidatedByConfigChange(t *testing.T) {
	var gen uint64
	var filtering bool
	srv := testServerOpts(t, Options{
		FilterAdult: func() bool { return filtering },
		ConfigGen:   func() uint64 { return gen },
		StatsTTL:    time.Hour, // long enough that only the generation can bust it
	})
	if m := getJSON(t, srv.URL+"/api/stats"); m["filter_adult"] != false {
		t.Fatalf("initial filter_adult = %v, want false", m["filter_adult"])
	}
	// Change the setting without bumping the generation: the cache legitimately
	// still holds, which is what makes the next step meaningful.
	filtering = true
	if m := getJSON(t, srv.URL+"/api/stats"); m["filter_adult"] != false {
		t.Errorf("cache did not hold within its TTL: %v", m["filter_adult"])
	}
	gen++
	if m := getJSON(t, srv.URL+"/api/stats"); m["filter_adult"] != true {
		t.Errorf("filter_adult = %v after a config change, want true", m["filter_adult"])
	}
}
