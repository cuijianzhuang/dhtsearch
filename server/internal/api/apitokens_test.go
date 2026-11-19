package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"dhtsearch/server/internal/filter"
	"dhtsearch/server/internal/settings"
	"dhtsearch/server/internal/store"
)

// tokenFixture serves the API with an admin token, a settings
// manager and one seeded torrent, all over an in-memory store.
func tokenFixture(t *testing.T) (string, *settings.Manager, *store.Store) {
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
	mgr, err := settings.New(map[string]string{}, map[string]bool{}, st, nil)
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

func strptr(s string) *string { return &s }

func setPublicAPI(t *testing.T, base, key, value string) {
	t.Helper()
	code, body := putSettings(t, base, testAdminToken, map[string]*string{key: strptr(value)})
	if code != 200 {
		t.Fatalf("PUT settings %s=%s: %d %v", key, value, code, body)
	}
}

// seedToken inserts a token with a known plaintext directly through
// the store and returns the plaintext.
func seedToken(t *testing.T, st *store.Store, plaintext string, ratePerMin int) store.APIToken {
	t.Helper()
	tok, err := st.CreateAPIToken(context.Background(), "test-token",
		apiTokenDisplayPrefix(plaintext), apiTokenHash(plaintext), ratePerMin, 1000)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func doSearch(t *testing.T, base, token, header string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, base+"/api/search?q=Big", nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		if header == "x-api-key" {
			req.Header.Set("X-API-Key", token)
		} else {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func TestPublicGateMatrixRequireOff(t *testing.T) {
	base, _, st := tokenFixture(t)
	const plain = "dht_aaaaaaaabbbbbbbbccccccccdddddddd"
	tok := seedToken(t, st, plain, 600)

	if code := doSearch(t, base, "", ""); code != 200 {
		t.Fatalf("anonymous: %d, want 200", code)
	}
	if code := doSearch(t, base, "dht_wrongwrongwrong", ""); code != 200 {
		t.Fatalf("wrong token while not required: %d, want 200 (ignored)", code)
	}
	if code := doSearch(t, base, plain, ""); code != 200 {
		t.Fatalf("valid token (bearer): %d, want 200", code)
	}
	if code := doSearch(t, base, plain, "x-api-key"); code != 200 {
		t.Fatalf("valid token (x-api-key): %d, want 200", code)
	}
	// Both valid-token calls were counted even without enforcement.
	got, err := st.APITokenByHash(context.Background(), apiTokenHash(plain))
	if err != nil || got == nil {
		t.Fatalf("lookup: %v %+v", err, got)
	}
	if got.CallCount != 2 || got.LastUsedAt == 0 {
		t.Fatalf("counting: %+v, want call_count=2 and last_used_at set", got)
	}
	_ = tok
}

func TestPublicGateMatrixRequireOn(t *testing.T) {
	base, _, st := tokenFixture(t)
	const plain = "dht_11111111222222223333333344444444"
	tok := seedToken(t, st, plain, 600)
	setPublicAPI(t, base, settings.KeyPublicAPIRequireToken, "true")

	if code := doSearch(t, base, "", ""); code != 401 {
		t.Fatalf("missing token: %d, want 401", code)
	}
	if code := doSearch(t, base, "dht_nope", ""); code != 401 {
		t.Fatalf("wrong token: %d, want 401", code)
	}
	if code := doSearch(t, base, plain, ""); code != 200 {
		t.Fatalf("valid token: %d, want 200", code)
	}
	if code := doSearch(t, base, plain, "x-api-key"); code != 200 {
		t.Fatalf("valid token via X-API-Key: %d, want 200", code)
	}

	ctx := context.Background()
	if _, err := st.SetAPITokenEnabled(ctx, tok.ID, false); err != nil {
		t.Fatal(err)
	}
	if code := doSearch(t, base, plain, ""); code != 403 {
		t.Fatalf("disabled token: %d, want 403", code)
	}
	if _, err := st.SetAPITokenEnabled(ctx, tok.ID, true); err != nil {
		t.Fatal(err)
	}
	if code := doSearch(t, base, plain, ""); code != 200 {
		t.Fatalf("re-enabled token: %d, want 200", code)
	}
}

func TestPublicGateDisabled(t *testing.T) {
	base, _, _ := tokenFixture(t)
	setPublicAPI(t, base, settings.KeyPublicAPIEnabled, "false")

	resp, err := http.Get(base + "/api/search?q=Big")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 503 {
		t.Fatalf("search while disabled: %d, want 503", resp.StatusCode)
	}
	// Health and admin stay up.
	if m := getJSON(t, base+"/api/healthz"); m["ok"] != true {
		t.Fatalf("healthz while disabled: %v", m)
	}
	req, _ := http.NewRequest(http.MethodGet, base+"/api/admin/overview", nil)
	req.Header.Set("Authorization", "Bearer "+testAdminToken)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != 200 {
		t.Fatalf("admin overview while public api disabled: %d, want 200", resp2.StatusCode)
	}
}

func TestPublicGateTokenRateLimit(t *testing.T) {
	base, _, st := tokenFixture(t)
	const plain = "dht_ratelimitedtoken00000000000000"
	seedToken(t, st, plain, 2) // 2 requests per minute
	setPublicAPI(t, base, settings.KeyPublicAPIRequireToken, "true")

	for i := 1; i <= 2; i++ {
		if code := doSearch(t, base, plain, ""); code != 200 {
			t.Fatalf("call %d: %d, want 200", i, code)
		}
	}
	if code := doSearch(t, base, plain, ""); code != 429 {
		t.Fatalf("call 3 over quota: %d, want 429", code)
	}
	// The rejected call was not counted.
	got, _ := st.APITokenByHash(context.Background(), apiTokenHash(plain))
	if got == nil || got.CallCount != 2 {
		t.Fatalf("counting under rate limit: %+v, want call_count=2", got)
	}
}

// adminCall performs one admin API request and returns status + body.
func adminCall(t *testing.T, base, method, path, adminToken string, payload any) (int, []byte) {
	t.Helper()
	var body io.Reader
	if payload != nil {
		b, _ := json.Marshal(payload)
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, base+path, body)
	if err != nil {
		t.Fatal(err)
	}
	if adminToken != "" {
		req.Header.Set("Authorization", "Bearer "+adminToken)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

func TestAdminAPITokenLifecycle(t *testing.T) {
	base, _, _ := tokenFixture(t)

	// Admin auth still gates everything.
	if code, _ := adminCall(t, base, http.MethodGet, "/api/admin/api-tokens", "", nil); code != 401 {
		t.Fatalf("list without admin token: %d, want 401", code)
	}
	if code, _ := adminCall(t, base, http.MethodPost, "/api/admin/api-tokens", testAdminToken,
		map[string]any{"name": ""}); code != 400 {
		t.Fatalf("create with empty name: %d, want 400", code)
	}

	// Create: plaintext comes back exactly once.
	code, raw := adminCall(t, base, http.MethodPost, "/api/admin/api-tokens", testAdminToken,
		map[string]any{"name": "partner-a"})
	if code != 201 {
		t.Fatalf("create: %d %s", code, raw)
	}
	var created struct {
		Token string         `json:"token"`
		Item  store.APIToken `json:"item"`
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatal(err)
	}
	plain := created.Token
	if !strings.HasPrefix(plain, "dht_") || len(plain) != 68 {
		t.Fatalf("plaintext shape: %q", plain)
	}
	if created.Item.Prefix != apiTokenDisplayPrefix(plain) || created.Item.RatePerMin != 600 || !created.Item.Enabled {
		t.Fatalf("created item: %+v", created.Item)
	}
	id := created.Item.ID

	// The list must never carry the plaintext or the hash.
	code, raw = adminCall(t, base, http.MethodGet, "/api/admin/api-tokens", testAdminToken, nil)
	if code != 200 {
		t.Fatalf("list: %d", code)
	}
	if strings.Contains(string(raw), plain) || strings.Contains(string(raw), apiTokenHash(plain)) {
		t.Fatalf("list leaks secret material: %s", raw)
	}
	if !strings.Contains(string(raw), created.Item.Prefix) {
		t.Fatalf("list missing prefix: %s", raw)
	}

	// The token works, then stops the moment it is disabled, works
	// again when re-enabled, and dies for good when deleted.
	setPublicAPI(t, base, settings.KeyPublicAPIRequireToken, "true")
	if code := doSearch(t, base, plain, ""); code != 200 {
		t.Fatalf("fresh token: %d, want 200", code)
	}
	idPath := "/api/admin/api-tokens/" + strconv.FormatInt(id, 10)
	if code, _ := adminCall(t, base, http.MethodPost, idPath+"/disable", testAdminToken, nil); code != 200 {
		t.Fatalf("disable: %d", code)
	}
	if code := doSearch(t, base, plain, ""); code != 403 {
		t.Fatalf("disabled token: %d, want 403", code)
	}
	if code, _ := adminCall(t, base, http.MethodPost, idPath+"/enable", testAdminToken, nil); code != 200 {
		t.Fatalf("enable: %d", code)
	}
	if code, raw := adminCall(t, base, http.MethodPut, idPath+"/rate", testAdminToken,
		map[string]any{"rate_per_min": 5}); code != 200 {
		t.Fatalf("set rate: %d %s", code, raw)
	}
	if code := doSearch(t, base, plain, ""); code != 200 {
		t.Fatalf("re-enabled token: %d, want 200", code)
	}
	if code, _ := adminCall(t, base, http.MethodDelete, idPath, testAdminToken, nil); code != 200 {
		t.Fatalf("delete: %d", code)
	}
	if code := doSearch(t, base, plain, ""); code != 401 {
		t.Fatalf("deleted token: %d, want 401", code)
	}
	if code, _ := adminCall(t, base, http.MethodDelete, idPath, testAdminToken, nil); code != 404 {
		t.Fatalf("delete again: %d, want 404", code)
	}
}
