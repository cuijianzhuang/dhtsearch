package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"dhtsearch/server/internal/filter"
	"dhtsearch/server/internal/settings"
	"dhtsearch/server/internal/store"
)

// settingsFixture serves the API with an admin token and a settings
// manager backed by the same in-memory store.
func settingsFixture(t *testing.T) (string, *settings.Manager, *store.Store) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Upsert(store.Torrent{
		InfoHash: strings.Repeat("a", 40), Name: "Big Buck Bunny",
		TotalSize: 1 << 30, FileCount: 1,
		Files: []filter.File{{Path: "bbb.mkv", Size: 1 << 30}}, CreatedAt: 100,
	}); err != nil {
		t.Fatal(err)
	}
	mgr, err := settings.New(
		map[string]string{settings.KeyOpenAIAPIKey: "topsecret-value"},
		map[string]bool{settings.KeyOpenAIAPIKey: true},
		st, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(st, nil, nil, Options{
		AdminToken: testAdminToken,
		Settings:   mgr,
	}).Handler())
	t.Cleanup(srv.Close)
	return srv.URL, mgr, st
}

func putSettings(t *testing.T, base, token string, values map[string]*string) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"values": values})
	req, err := http.NewRequest(http.MethodPut, base+"/api/admin/settings", bytes.NewReader(b))
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
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func strp(s string) *string { return &s }

func TestAdminSettingsAuthAndMasking(t *testing.T) {
	base, _, _ := settingsFixture(t)

	// No token: 401.
	resp, err := http.Get(base + "/api/admin/settings")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated GET = %d, want 401", resp.StatusCode)
	}

	code, body := adminReq(t, http.MethodGet, base+"/api/admin/settings", testAdminToken, nil)
	if code != http.StatusOK {
		t.Fatalf("GET = %d (%v)", code, body)
	}
	raw, _ := json.Marshal(body)
	if strings.Contains(string(raw), "topsecret-value") {
		t.Fatal("GET settings leaked the API key")
	}
	// Find the OPENAI_API_KEY item and check its masked shape.
	found := false
	for _, g := range body["groups"].([]any) {
		for _, it := range g.(map[string]any)["items"].([]any) {
			item := it.(map[string]any)
			if item["key"] == settings.KeyOpenAIAPIKey {
				found = true
				if item["value"] != "" || item["set"] != true || item["source"] != "env" {
					t.Fatalf("masked item = %v", item)
				}
			}
			if item["key"] == settings.KeyHTTPAddr && item["editable"] != false {
				t.Fatalf("HTTP_ADDR must be read-only: %v", item)
			}
		}
	}
	if !found {
		t.Fatal("OPENAI_API_KEY missing from snapshot")
	}
}

func TestAdminSettingsPutValidationAndApply(t *testing.T) {
	base, mgr, _ := settingsFixture(t)

	// One bad value rejects the whole batch with per-key errors.
	code, body := putSettings(t, base, testAdminToken, map[string]*string{
		settings.KeySiteName:    strp("My Search"),
		settings.KeyMetaWorkers: strp("-5"),
	})
	if code != http.StatusBadRequest {
		t.Fatalf("PUT = %d (%v), want 400", code, body)
	}
	errs, _ := body["errors"].(map[string]any)
	if errs[settings.KeyMetaWorkers] == "" {
		t.Fatalf("errors = %v, want a META_WORKERS entry", body)
	}
	if got := mgr.Get(settings.KeySiteName); got != "DHTSearch" {
		t.Fatalf("rejected batch partially applied: SITE_NAME = %q", got)
	}

	// A clean batch applies; live vs restart keys are reported apart.
	code, body = putSettings(t, base, testAdminToken, map[string]*string{
		settings.KeySiteName:    strp("My Search"),
		settings.KeyMetaWorkers: strp("64"),
	})
	if code != http.StatusOK {
		t.Fatalf("PUT = %d (%v)", code, body)
	}
	applied, _ := body["applied"].([]any)
	restart, _ := body["restart_required"].([]any)
	if len(applied) != 1 || applied[0] != settings.KeySiteName {
		t.Fatalf("applied = %v", body)
	}
	if len(restart) != 1 || restart[0] != settings.KeyMetaWorkers {
		t.Fatalf("restart_required = %v", body)
	}
	if got := mgr.Get(settings.KeySiteName); got != "My Search" {
		t.Fatalf("SITE_NAME = %q after PUT", got)
	}

	// /api/site reflects the new presentation values publicly.
	resp, err := http.Get(base + "/api/site")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var site map[string]string
	json.NewDecoder(resp.Body).Decode(&site)
	if site["name"] != "My Search" || site["theme_color"] != "teal" || site["theme_appearance"] != "system" {
		t.Fatalf("/api/site = %v", site)
	}
}

func TestAdminSettingsRateLimitHotSwap(t *testing.T) {
	base, _, _ := settingsFixture(t)

	// Squeeze the limiter to a single-token bucket at a crawl rate.
	code, _ := putSettings(t, base, testAdminToken, map[string]*string{
		settings.KeyRateLimitRPS:   strp("0.01"),
		settings.KeyRateLimitBurst: strp("1"),
	})
	if code != http.StatusOK {
		t.Fatalf("PUT = %d", code)
	}
	// The rebuild happens on the next request: the first search takes
	// the only token, the second is rejected — no restart involved.
	statuses := []int{}
	for i := 0; i < 2; i++ {
		resp, err := http.Get(base + "/api/search?q=bunny")
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		statuses = append(statuses, resp.StatusCode)
	}
	if statuses[0] != http.StatusOK || statuses[1] != http.StatusTooManyRequests {
		t.Fatalf("statuses = %v, want [200 429] after hot swap", statuses)
	}
}

func TestAdminSettingsDisabledWithoutToken(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	mgr, err := settings.New(nil, nil, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(st, nil, nil, Options{Settings: mgr}).Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/admin/settings")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("GET without admin token = %d, want 503", resp.StatusCode)
	}
	// /api/site stays public and serves defaults.
	resp2, err := http.Get(srv.URL + "/api/site")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	var site map[string]string
	json.NewDecoder(resp2.Body).Decode(&site)
	if site["name"] != "DHTSearch" {
		t.Fatalf("/api/site = %v", site)
	}
}
