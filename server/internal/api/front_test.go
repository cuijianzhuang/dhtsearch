package api

import (
	"encoding/json"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"dhtsearch/server/internal/filter"
	"dhtsearch/server/internal/store"
)

// frontFixture seeds a mixed-category library and serves the API.
func frontFixture(t *testing.T, opts Options) (string, *store.Store) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	recs := []store.Torrent{
		{InfoHash: strings.Repeat("a", 40), Name: "Alpha Movie <2024> & Friends", TotalSize: 2 << 30, FileCount: 1, CreatedAt: 100,
			Files: []filter.File{{Path: "alpha.mkv", Size: 2 << 30}}},
		{InfoHash: strings.Repeat("b", 40), Name: "Beta Movie", TotalSize: 1 << 30, FileCount: 1, CreatedAt: 200,
			Files: []filter.File{{Path: "beta.mp4", Size: 1 << 30}}},
		{InfoHash: strings.Repeat("c", 40), Name: "Alpha Song", TotalSize: 3 << 30, FileCount: 1, CreatedAt: 300,
			Files: []filter.File{{Path: "song.flac", Size: 3 << 30}}},
		{InfoHash: strings.Repeat("d", 40), Name: "Ubuntu ISO", TotalSize: 5 << 30, FileCount: 1, CreatedAt: 400,
			Files: []filter.File{{Path: "ubuntu.iso", Size: 5 << 30}}},
	}
	for _, r := range recs {
		if err := st.Upsert(r); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(New(st, nil, nil, opts).Handler())
	t.Cleanup(srv.Close)
	return srv.URL, st
}

func TestSearchCategoryAndSort(t *testing.T) {
	base, _ := frontFixture(t, Options{})

	m := getJSON(t, base+"/api/search?q=&category=video")
	if m["total"].(float64) != 2 {
		t.Fatalf("video total: %v", m["total"])
	}
	cats := m["categories"].(map[string]any)
	if cats["video"].(float64) != 2 || cats["audio"].(float64) != 1 || cats["software"].(float64) != 1 {
		t.Fatalf("categories: %v", cats)
	}
	first := m["results"].([]any)[0].(map[string]any)
	if first["category"] != "video" {
		t.Fatalf("result category: %v", first["category"])
	}

	// Sort by size: the 5 GiB ISO leads.
	m = getJSON(t, base+"/api/search?q=&sort=size")
	first = m["results"].([]any)[0].(map[string]any)
	if first["name"] != "Ubuntu ISO" {
		t.Fatalf("size sort first: %v", first["name"])
	}
	if m["sort"] != "size" {
		t.Fatalf("sort echo: %v", m["sort"])
	}

	// Keyword + category counts describe the keyword matches.
	m = getJSON(t, base+"/api/search?q=alpha&category=audio")
	if m["total"].(float64) != 1 {
		t.Fatalf("alpha+audio total: %v", m["total"])
	}
	cats = m["categories"].(map[string]any)
	if cats["video"].(float64) != 1 || cats["audio"].(float64) != 1 {
		t.Fatalf("alpha categories: %v", cats)
	}

	// Invalid parameters are 400s, not silent fallbacks.
	for _, u := range []string{"/api/search?category=movies", "/api/search?sort=popular", "/rss?category=x"} {
		resp, err := http.Get(base + u)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", u, resp.StatusCode)
		}
	}
}

func TestSearchMagnetCarriesTrackers(t *testing.T) {
	base, _ := frontFixture(t, Options{
		Trackers: func() []string { return []string{"udp://tracker.example.org:1337/announce"} },
	})
	m := getJSON(t, base+"/api/search?q=beta")
	first := m["results"].([]any)[0].(map[string]any)
	magnet := first["magnet"].(string)
	if !strings.Contains(magnet, "&tr=udp%3A%2F%2Ftracker.example.org%3A1337%2Fannounce") {
		t.Fatalf("magnet missing tracker: %s", magnet)
	}
	trs := m["trackers"].([]any)
	if len(trs) != 1 || trs[0] != "udp://tracker.example.org:1337/announce" {
		t.Fatalf("trackers field: %v", trs)
	}
}

func TestRSSFeed(t *testing.T) {
	base, _ := frontFixture(t, Options{})
	resp, err := http.Get(base + "/rss?q=alpha")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/rss+xml") {
		t.Fatalf("content type: %s", ct)
	}
	var doc struct {
		XMLName xml.Name `xml:"rss"`
		Channel struct {
			Title string `xml:"title"`
			Items []struct {
				Title string `xml:"title"`
				Link  string `xml:"link"`
				GUID  string `xml:"guid"`
			} `xml:"item"`
		} `xml:"channel"`
	}
	if err := xml.NewDecoder(resp.Body).Decode(&doc); err != nil {
		t.Fatalf("RSS does not parse: %v", err)
	}
	if len(doc.Channel.Items) != 2 {
		t.Fatalf("items = %d, want 2 (alpha matches)", len(doc.Channel.Items))
	}
	// Newest first, magnet link, special chars survived escaping.
	if doc.Channel.Items[0].Title != "Alpha Song" {
		t.Errorf("first item = %q, want Alpha Song (newest)", doc.Channel.Items[0].Title)
	}
	found := false
	for _, it := range doc.Channel.Items {
		if it.Title == "Alpha Movie <2024> & Friends" {
			found = true
			if !strings.HasPrefix(it.Link, "magnet:?xt=urn:btih:") {
				t.Errorf("item link not a magnet: %s", it.Link)
			}
		}
	}
	if !found {
		t.Error("escaped-title item missing from feed")
	}
	// No keyword: the whole library, still capped and newest-first.
	resp2, err := http.Get(base + "/rss")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	var doc2 struct {
		Channel struct {
			Items []struct {
				Title string `xml:"title"`
			} `xml:"item"`
		} `xml:"channel"`
	}
	if err := xml.NewDecoder(resp2.Body).Decode(&doc2); err != nil {
		t.Fatal(err)
	}
	if len(doc2.Channel.Items) != 4 || doc2.Channel.Items[0].Title != "Ubuntu ISO" {
		t.Errorf("firehose feed: %d items, first %v", len(doc2.Channel.Items), doc2.Channel.Items)
	}
}

func TestSimilarEndpoint(t *testing.T) {
	base, _ := frontFixture(t, Options{})
	m := getJSON(t, base+"/api/torrents/"+strings.Repeat("a", 40)+"/similar")
	results := m["results"].([]any)
	if len(results) == 0 {
		t.Fatal("no similar results")
	}
	if results[0].(map[string]any)["name"] != "Beta Movie" {
		t.Errorf("first similar: %v", results[0])
	}
	resp, err := http.Get(base + "/api/torrents/" + strings.Repeat("f", 40) + "/similar")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("missing torrent similar: status %d, want 404", resp.StatusCode)
	}
}

func postReport(t *testing.T, base string, body map[string]any) int {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(base+"/api/report", "application/json", strings.NewReader(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestReportFlowAndAdminQueue(t *testing.T) {
	base, _ := frontFixture(t, Options{AdminToken: testAdminToken})
	hash := strings.Repeat("a", 40)

	// Bad inputs first.
	if code := postReport(t, base, map[string]any{"info_hash": "xyz", "reason": "adult"}); code != 400 {
		t.Errorf("bad hash: %d, want 400", code)
	}
	if code := postReport(t, base, map[string]any{"info_hash": hash, "reason": "rude"}); code != 400 {
		t.Errorf("bad reason: %d, want 400", code)
	}
	if code := postReport(t, base, map[string]any{"info_hash": strings.Repeat("f", 40), "reason": "adult"}); code != 404 {
		t.Errorf("unknown torrent: %d, want 404", code)
	}

	// Three good reports pass (burst), the fourth is throttled.
	for i := 0; i < 3; i++ {
		if code := postReport(t, base, map[string]any{"info_hash": hash, "reason": "adult", "note": "bad"}); code != 201 {
			t.Fatalf("report %d: %d, want 201", i, code)
		}
	}
	if code := postReport(t, base, map[string]any{"info_hash": hash, "reason": "spam"}); code != 429 {
		t.Fatalf("fourth report: %d, want 429", code)
	}

	// The queue shows them; overview counts them.
	code, m := adminReq(t, "GET", base+"/api/admin/reports", testAdminToken, nil)
	if code != 200 || m["total"].(float64) != 3 {
		t.Fatalf("queue: %d %v", code, m)
	}
	first := m["results"].([]any)[0].(map[string]any)
	if first["name"] != "Alpha Movie <2024> & Friends" || first["reason"] != "adult" {
		t.Errorf("queue item: %v", first)
	}
	id := int64(first["id"].(float64))

	code, ov := adminReq(t, "GET", base+"/api/admin/overview", testAdminToken, nil)
	if code != 200 || ov["reports_pending"].(float64) != 3 {
		t.Fatalf("overview reports_pending: %d %v", code, ov["reports_pending"])
	}

	// Dismiss one, block through another: the torrent disappears from
	// search and its report resolves.
	code, _ = adminReq(t, "POST", base+"/api/admin/reports/1/dismiss", testAdminToken, nil)
	if code != 200 {
		t.Fatalf("dismiss: %d", code)
	}
	code, m = adminReq(t, "POST", base+"/api/admin/reports/"+strconv.FormatInt(id, 10)+"/block", testAdminToken, nil)
	if code != 200 || m["deleted"].(float64) != 1 {
		t.Fatalf("block via report: %d %v", code, m)
	}
	search := getJSON(t, base+"/api/search?q=alpha&category=video")
	if search["total"].(float64) != 0 {
		t.Errorf("blocked torrent still searchable: %v", search["total"])
	}
	code, m = adminReq(t, "GET", base+"/api/admin/reports?status=pending", testAdminToken, nil)
	if code != 200 || m["total"].(float64) != 1 {
		t.Fatalf("pending after actions: %d %v", code, m["total"])
	}
	// Admin endpoints still require the token.
	code, _ = adminReq(t, "GET", base+"/api/admin/reports", "", nil)
	if code != 401 {
		t.Errorf("queue without token: %d, want 401", code)
	}
}
