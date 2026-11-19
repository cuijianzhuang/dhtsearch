package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"dhtsearch/server/internal/settings"
	"dhtsearch/server/internal/store"
)

func TestAdminModerationState(t *testing.T) {
	base, _, st := settingsFixture(t)

	// No token -> 401; wrong token -> 401.
	if code, _ := adminReq(t, http.MethodGet, base+"/api/admin/moderation", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", code)
	}
	if code, _ := adminReq(t, http.MethodGet, base+"/api/admin/moderation", "wrong", nil); code != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d", code)
	}

	// Seed audit-log rows and counters, then read the state back.
	if err := st.LogModeration([]store.ModerationRecord{
		{InfoHash: "h1", Name: "Clean Movie", Label: "ok", Action: "kept"},
		{InfoHash: "h2", Name: "Bad Movie", Label: "adult", Action: "deleted"},
	}, 42); err != nil {
		t.Fatal(err)
	}
	st.IncrStat("llm_reviewed", 7)
	st.IncrStat("llm_adult_removed", 2)

	code, body := adminReq(t, http.MethodGet, base+"/api/admin/moderation", testAdminToken, nil)
	if code != http.StatusOK {
		t.Fatalf("state: %d (%v)", code, body)
	}
	if body["key_set"] != true {
		t.Fatalf("key_set = %v, want true (fixture seeds a key)", body["key_set"])
	}
	if body["model"] != "nvidia/nemotron-3-super-120b-a12b:free" {
		t.Fatalf("model = %v", body["model"])
	}
	stats, _ := body["stats"].(map[string]any)
	if stats["reviewed"] != float64(7) || stats["adult_removed"] != float64(2) {
		t.Fatalf("stats = %v", stats)
	}
	recent, _ := body["recent"].([]any)
	if len(recent) != 2 {
		t.Fatalf("recent = %v", body["recent"])
	}
	newest, _ := recent[0].(map[string]any)
	if newest["info_hash"] != "h2" || newest["action"] != "deleted" {
		t.Fatalf("newest recent = %v", newest)
	}
	// The stored key must not appear anywhere in the payload.
	raw, _ := json.Marshal(body)
	if strings.Contains(string(raw), "topsecret-value") {
		t.Fatalf("state leaked the API key: %s", raw)
	}
}

// TestAdminModerationTestProbe points the settings at a local fake
// OpenAI endpoint, probes it through the admin API, and checks the
// debounce rejects an immediate second probe.
func TestAdminModerationTestProbe(t *testing.T) {
	var gotModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		gotModel = req.Model
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer upstream.Close()

	base, mgr, _ := settingsFixture(t)
	if _, _, err := mgr.Update(map[string]*string{
		settings.KeyOpenAIBaseURL: strp(upstream.URL),
		settings.KeyOpenAIModel:   strp("probe-model"),
	}); err != nil {
		t.Fatal(err)
	}

	code, body := adminReq(t, http.MethodPost, base+"/api/admin/moderation/test", testAdminToken, map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("probe: %d (%v)", code, body)
	}
	if body["ok"] != true || body["model"] != "probe-model" {
		t.Fatalf("probe body = %v", body)
	}
	if gotModel != "probe-model" {
		t.Fatalf("upstream saw model %q", gotModel)
	}
	if _, hasLatency := body["latency_ms"]; !hasLatency {
		t.Fatalf("probe body lacks latency_ms: %v", body)
	}

	// Immediate second probe -> debounce 429.
	if code, _ := adminReq(t, http.MethodPost, base+"/api/admin/moderation/test", testAdminToken, map[string]any{}); code != http.StatusTooManyRequests {
		t.Fatalf("second probe: %d, want 429", code)
	}

	// Unauthenticated probe -> 401 (and must not reach the upstream).
	if code, _ := adminReq(t, http.MethodPost, base+"/api/admin/moderation/test", "", map[string]any{}); code != http.StatusUnauthorized {
		t.Fatalf("no token probe: %d", code)
	}
}

// TestAdminOverviewFilterSection: the overview carries the filter
// switches and counters once settings are wired, and a settings
// change is reflected on the next read.
func TestAdminOverviewFilterSection(t *testing.T) {
	base, mgr, st := settingsFixture(t)
	st.IncrStat("adult_filtered", 5)

	_, body := adminReq(t, http.MethodGet, base+"/api/admin/overview", testAdminToken, nil)
	f, _ := body["filter"].(map[string]any)
	if f == nil {
		t.Fatalf("overview lacks filter section: %v", body)
	}
	if f["enabled"] != true || f["adult_enabled"] != true || f["adult_filtered"] != float64(5) {
		t.Fatalf("filter section = %v", f)
	}

	if _, _, err := mgr.Update(map[string]*string{
		settings.KeyFilterAdultEnabled: strp("false"),
	}); err != nil {
		t.Fatal(err)
	}
	_, body = adminReq(t, http.MethodGet, base+"/api/admin/overview", testAdminToken, nil)
	f, _ = body["filter"].(map[string]any)
	if f["adult_enabled"] != false {
		t.Fatalf("after update, filter section = %v", f)
	}

	// The new keys are in the settings snapshot, in the filter group.
	code, snap := adminReq(t, http.MethodGet, base+"/api/admin/settings", testAdminToken, nil)
	if code != http.StatusOK {
		t.Fatalf("settings snapshot: %d", code)
	}
	groups, _ := snap["groups"].([]any)
	var filterKeys []string
	for _, g := range groups {
		gm, _ := g.(map[string]any)
		if gm["key"] == "filter" {
			for _, it := range gm["items"].([]any) {
				filterKeys = append(filterKeys, it.(map[string]any)["key"].(string))
			}
		}
		if gm["key"] == "moderation" {
			for _, it := range gm["items"].([]any) {
				if it.(map[string]any)["key"] == "FILTER_BLOCK_WORDS" {
					t.Fatalf("FILTER_BLOCK_WORDS still in moderation group")
				}
			}
		}
	}
	for _, want := range []string{"FILTER_ENABLED", "FILTER_ADULT_ENABLED", "FILTER_SPAM_ENABLED",
		"FILTER_TOO_SMALL_ENABLED", "FILTER_BLOCK_WORDS", "FILTER_ALLOW_WORDS"} {
		found := false
		for _, k := range filterKeys {
			if k == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("filter group keys %v lack %s", filterKeys, want)
		}
	}
}
