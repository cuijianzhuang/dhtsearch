package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"dhtsearch/server/internal/searchstats"
	"dhtsearch/server/internal/settings"
	"dhtsearch/server/internal/store"
)

// stubRecorder captures what the search handler counted.
type stubRecorder struct {
	mu    sync.Mutex
	calls [][2]string
}

func (s *stubRecorder) Record(term, display string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, [2]string{term, display})
}

func (s *stubRecorder) got() [][2]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][2]string(nil), s.calls...)
}

func TestSearchCountsSuccessfulQueries(t *testing.T) {
	rec := &stubRecorder{}
	base := testServerOpts(t, Options{SearchTerms: rec}).URL

	getJSON(t, base+"/api/search?q=Ubuntu")
	getJSON(t, base+"/api/search?q=%20%20UBUNTU%20%20Desktop")
	// The browse path (no query) and an empty query count nothing.
	getJSON(t, base+"/api/search")
	getJSON(t, base+"/api/search?q=%20%20")
	// A rejected search (invalid category) counts nothing.
	resp, err := http.Get(base + "/api/search?q=Ubuntu&category=bogus")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid category: %d, want 400", resp.StatusCode)
	}

	want := [][2]string{
		{"ubuntu", "Ubuntu"},
		{"ubuntu desktop", "UBUNTU Desktop"},
	}
	got := rec.got()
	if len(got) != len(want) {
		t.Fatalf("recorded = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("recorded[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

// TestSearchCountingNeverFailsSearch wires the real counting
// service with its store already broken: Record is in-memory, so
// searches keep answering 200 — counting can only lose data (its
// flush keeps the batch), never take search down with it.
func TestSearchCountingNeverFailsSearch(t *testing.T) {
	broken, err := store.Open(filepath.Join(t.TempDir(), "broken.db"))
	if err != nil {
		t.Fatal(err)
	}
	svc := searchstats.New(broken, nil)
	broken.Close() // every flush will fail from here on

	base := testServerOpts(t, Options{SearchTerms: svc}).URL
	m := getJSON(t, base+"/api/search?q=Ubuntu")
	if m["total"].(float64) != 1 { // only "Ubuntu 24.04" matches
		t.Fatalf("search with broken counting store: %v", m)
	}
}

func trendingTermsJSON(t *testing.T, url string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, m
}

func TestTrendingTermsEndpoint(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	today := store.SearchTermDay(time.Now())
	seed := func(day int64, deltas ...store.SearchTermDelta) {
		if err := st.RecordSearchTerms(ctx, day, deltas); err != nil {
			t.Fatal(err)
		}
	}
	seed(today,
		store.SearchTermDelta{Term: "ubuntu", Display: "Ubuntu", Count: 5},
		store.SearchTermDelta{Term: "dune", Display: "Dune", Count: 2})
	seed(today-3, store.SearchTermDelta{Term: "ubuntu", Display: "ubuntu", Count: 10})

	srv := httptest.NewServer(New(st, func() CrawlerStatus { return CrawlerStatus{} }, nil, Options{}).Handler())
	t.Cleanup(srv.Close)

	// Default window is today.
	code, m := trendingTermsJSON(t, srv.URL+"/api/trending-terms")
	if code != 200 || m["window"] != "today" {
		t.Fatalf("default: code=%d body=%v", code, m)
	}
	terms := m["terms"].([]any)
	if len(terms) != 2 {
		t.Fatalf("today terms = %v", terms)
	}
	first := terms[0].(map[string]any)
	if first["term"] != "Ubuntu" || first["count"].(float64) != 5 {
		t.Fatalf("today first = %v, want Ubuntu ×5", first)
	}

	// The 7d window sums across day buckets.
	code, m = trendingTermsJSON(t, srv.URL+"/api/trending-terms?window=7d")
	if code != 200 || m["window"] != "7d" {
		t.Fatalf("7d: code=%d body=%v", code, m)
	}
	terms = m["terms"].([]any)
	first = terms[0].(map[string]any)
	if first["term"] != "Ubuntu" || first["count"].(float64) != 15 {
		t.Fatalf("7d first = %v, want Ubuntu ×15", first)
	}

	// Limit clamps the list; a bogus window is a 400.
	_, m = trendingTermsJSON(t, srv.URL+"/api/trending-terms?limit=1")
	if terms := m["terms"].([]any); len(terms) != 1 {
		t.Fatalf("limit=1 terms = %v", terms)
	}
	if code, _ := trendingTermsJSON(t, srv.URL+"/api/trending-terms?window=30d"); code != 400 {
		t.Fatalf("window=30d: %d, want 400", code)
	}
}

// TestTrendingTermsEmptyAndGated: a fresh install serves an empty
// (non-null) list, and the endpoint sits behind the same public
// gate as its siblings — with the master switch off it answers 503
// even to first-party callers.
func TestTrendingTermsEmptyAndGated(t *testing.T) {
	base := testServer(t).URL
	code, m := trendingTermsJSON(t, base+"/api/trending-terms")
	if code != 200 {
		t.Fatalf("empty: %d", code)
	}
	if terms, ok := m["terms"].([]any); !ok || len(terms) != 0 {
		t.Fatalf("empty terms = %v, want []", m["terms"])
	}

	gatedBase, _, _ := tokenFixtureInternal(t, testInternalToken)
	setPublicAPI(t, gatedBase, settings.KeyPublicAPIEnabled, "false")
	if code := doInternal(t, gatedBase, "/api/trending-terms", testInternalToken); code != 503 {
		t.Fatalf("master switch off: %d, want 503", code)
	}
}
