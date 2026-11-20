package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"dhtsearch/server/internal/settings"
	"dhtsearch/server/internal/store"
)

// updateFixture serves the API with an admin token, a settings
// manager and a real (temporary) data dir for the contract files.
func updateFixture(t *testing.T) (*Server, string, string) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	mgr, err := settings.New(nil, nil, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	s := New(st, nil, nil, Options{
		AdminToken: testAdminToken,
		Settings:   mgr,
		DataDir:    dir,
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, ts.URL, dir
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v0.1.6", "v0.1.7", -1},
		{"v0.1.7", "v0.1.6", 1},
		{"v0.1.6", "v0.1.6", 0},
		{"0.1.6", "v0.1.6", 0},   // leading v is cosmetic
		{"v0.1.10", "v0.1.9", 1}, // numeric, not lexicographic
		{"v0.2", "v0.1.9", 1},
		{"v1", "v1.0.0", 0}, // missing components count as 0
		{"dev-abc", "v0.1.0", -1},
	}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestKVContractRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if _, ok := parseKVFile(filepath.Join(dir, "nope")); ok {
		t.Fatal("missing file reported as present")
	}
	err := writeKVFileAtomic(dir, "update-panel", [][2]string{
		{"ENABLED", "1"},
		{"TIME", "04:15"},
		{"MESSAGE", "line one\nline two"},
	})
	if err != nil {
		t.Fatal(err)
	}
	kv, ok := parseKVFile(filepath.Join(dir, "update-panel"))
	if !ok {
		t.Fatal("written file not found")
	}
	if kv["ENABLED"] != "1" || kv["TIME"] != "04:15" {
		t.Fatalf("round trip mismatch: %v", kv)
	}
	if strings.Contains(kv["MESSAGE"], "\n") {
		t.Fatalf("value with newline not sanitized: %q", kv["MESSAGE"])
	}
	// No temp files are left behind.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
}

func TestUpdateEndpointsWithMockGitHub(t *testing.T) {
	s, base, dir := updateFixture(t)
	writeFile(t, dir, "VERSION", "v0.1.6\n")

	var hits atomic.Int32
	var fail atomic.Bool
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("User-Agent") != "dhtsearch-update-check" {
			t.Errorf("missing update-check user agent: %q", r.Header.Get("User-Agent"))
		}
		if fail.Load() {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"tag_name":"v0.1.7","published_at":"2026-01-02T03:04:05Z"}`)
	}))
	t.Cleanup(gh.Close)
	s.upd.url = gh.URL

	// Unauthenticated callers never reach the update endpoints.
	if code, _ := doReq(t, http.MethodGet, base+"/api/admin/update", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("no token: got %d, want 401", code)
	}

	code, body := doReq(t, http.MethodGet, base+"/api/admin/update", testAdminToken, nil)
	if code != http.StatusOK {
		t.Fatalf("GET update: got %d (%v)", code, body)
	}
	if body["current_version"] != "v0.1.6" || body["latest_version"] != "v0.1.7" {
		t.Fatalf("versions wrong: %v", body)
	}
	if body["update_available"] != true {
		t.Fatalf("update_available = %v, want true", body["update_available"])
	}
	if body["latest_published_at"] != "2026-01-02T03:04:05Z" {
		t.Fatalf("published_at = %v", body["latest_published_at"])
	}
	if hits.Load() != 1 {
		t.Fatalf("github hits = %d, want 1", hits.Load())
	}

	// A second read inside the TTL is served from cache.
	if _, _ = doReq(t, http.MethodGet, base+"/api/admin/update", testAdminToken, nil); hits.Load() != 1 {
		t.Fatalf("cached read hit github again: %d hits", hits.Load())
	}
	// The explicit check button forces a refresh.
	if code, _ := doReq(t, http.MethodPost, base+"/api/admin/update/check", testAdminToken, nil); code != http.StatusOK {
		t.Fatalf("POST check: got %d", code)
	}
	if hits.Load() != 2 {
		t.Fatalf("forced check did not re-query: %d hits", hits.Load())
	}

	// A failing GitHub keeps the last good result and reports the error.
	fail.Store(true)
	code, body = doReq(t, http.MethodPost, base+"/api/admin/update/check", testAdminToken, nil)
	if code != http.StatusOK {
		t.Fatalf("POST check (failing github): got %d", code)
	}
	if body["latest_version"] != "v0.1.7" {
		t.Fatalf("latest lost after failed check: %v", body["latest_version"])
	}
	if body["check_error"] == "" || body["check_error"] == nil {
		t.Fatalf("check_error empty after failed check: %v", body)
	}
}

func TestUpdateRequestLifecycle(t *testing.T) {
	_, base, dir := updateFixture(t)
	writeFile(t, dir, "VERSION", "v0.1.6\n")

	code, _ := doReq(t, http.MethodPost, base+"/api/admin/update/request", "", nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("request without token: got %d, want 401", code)
	}
	code, body := doReq(t, http.MethodPost, base+"/api/admin/update/request", testAdminToken, nil)
	if code != http.StatusAccepted {
		t.Fatalf("request: got %d (%v), want 202", code, body)
	}
	reqKV, ok := parseKVFile(filepath.Join(dir, updateRequestFileName))
	if !ok || reqKV["REQUEST_ID"] == "" {
		t.Fatalf("request flag not written: %v", reqKV)
	}
	up, _ := body["upgrade"].(map[string]any)
	if up["state"] != "requested" || up["pending"] != true {
		t.Fatalf("state after request: %v", up)
	}

	// A second request while one is pending conflicts.
	code, _ = doReq(t, http.MethodPost, base+"/api/admin/update/request", testAdminToken, nil)
	if code != http.StatusConflict {
		t.Fatalf("duplicate request: got %d, want 409", code)
	}

	// The agent consumes the flag and records the outcome.
	writeFile(t, dir, updateStatusFileName, strings.Join([]string{
		"STATE=succeeded",
		"REQUEST_ID=" + reqKV["REQUEST_ID"],
		"REQUESTED_AT=" + reqKV["REQUESTED_AT"],
		"STARTED_AT=2026-11-20 10:00:05",
		"FINISHED_AT=2026-11-20 10:01:00",
		"FROM_VERSION=v0.1.6",
		"TO_VERSION=v0.1.7",
		"MESSAGE=upgraded v0.1.6 -> v0.1.7",
		"",
	}, "\n"))
	if err := os.Remove(filepath.Join(dir, updateRequestFileName)); err != nil {
		t.Fatal(err)
	}
	_, body = doReq(t, http.MethodGet, base+"/api/admin/update", testAdminToken, nil)
	up, _ = body["upgrade"].(map[string]any)
	if up["state"] != "succeeded" || up["to_version"] != "v0.1.7" || up["pending"] == true {
		t.Fatalf("state after agent run: %v", up)
	}

	// While the agent reports a run in flight, new requests conflict.
	writeFile(t, dir, updateStatusFileName, "STATE=running\nREQUEST_ID=zzz\n")
	code, _ = doReq(t, http.MethodPost, base+"/api/admin/update/request", testAdminToken, nil)
	if code != http.StatusConflict {
		t.Fatalf("request during run: got %d, want 409", code)
	}
}

func TestUpdateProgressPassthrough(t *testing.T) {
	_, base, dir := updateFixture(t)
	writeFile(t, dir, "VERSION", "v0.1.6\n")

	get := func() map[string]any {
		_, body := doReq(t, http.MethodGet, base+"/api/admin/update", testAdminToken, nil)
		up, _ := body["upgrade"].(map[string]any)
		if up == nil {
			t.Fatalf("no upgrade object in payload: %v", body)
		}
		return up
	}

	// A running upgrade with stage markers: both pass through.
	writeFile(t, dir, updateStatusFileName, "STATE=running\nREQUEST_ID=r1\nPROGRESS=45\nPHASE=安装\n")
	if up := get(); up["progress"] != float64(45) || up["phase"] != "安装" {
		t.Fatalf("progress passthrough: %v", up)
	}

	// Out-of-range values are clamped to the bar's 0..100 domain.
	writeFile(t, dir, updateStatusFileName, "STATE=running\nREQUEST_ID=r1\nPROGRESS=140\nPHASE=安装\n")
	if up := get(); up["progress"] != float64(100) {
		t.Fatalf("progress clamp: %v", up)
	}

	// A malformed marker reads as absent, not as 0 or an error.
	writeFile(t, dir, updateStatusFileName, "STATE=running\nREQUEST_ID=r1\nPROGRESS=abc\n")
	if up := get(); up["progress"] != nil || up["phase"] != nil {
		t.Fatalf("malformed progress: %v", up)
	}

	// Older scripts write no markers at all: both fields are null.
	writeFile(t, dir, updateStatusFileName, "STATE=succeeded\nREQUEST_ID=r1\nMESSAGE=upgraded v0.1.5 -> v0.1.6\n")
	if up := get(); up["progress"] != nil || up["phase"] != nil {
		t.Fatalf("absent progress: %v", up)
	}

	// A fresh request superseding an old status must not inherit the
	// old run's bar.
	writeFile(t, dir, updateStatusFileName, "STATE=succeeded\nREQUEST_ID=old\nPROGRESS=100\nPHASE=完成\n")
	writeFile(t, dir, updateRequestFileName, "REQUEST_ID=new\nREQUESTED_AT=2026-11-20 11:00:00\nREQUESTED_BY=admin\n")
	if up := get(); up["state"] != "requested" || up["progress"] != nil || up["phase"] != nil {
		t.Fatalf("superseded progress: %v", up)
	}
}

func TestUpdateReviewerForbidden(t *testing.T) {
	fx := newAuthFixture(t, "", nil)
	fx.addUser(t, "rev", "rev-password", store.RoleReviewer, false)
	_, login := fx.passwordLogin(t, "rev", "rev-password")
	session := sessionOf(t, login)
	if code, _ := doReq(t, http.MethodGet, fx.url+"/api/admin/update", session, nil); code != http.StatusForbidden {
		t.Fatalf("reviewer GET update: got %d, want 403", code)
	}
	if code, _ := doReq(t, http.MethodPost, fx.url+"/api/admin/update/request", session, nil); code != http.StatusForbidden {
		t.Fatalf("reviewer POST request: got %d, want 403", code)
	}
	if code, _ := doReq(t, http.MethodPost, fx.url+"/api/admin/update/check", session, nil); code != http.StatusForbidden {
		t.Fatalf("reviewer POST check: got %d, want 403", code)
	}
}

func TestSettingsSyncWritesPanelFile(t *testing.T) {
	_, base, dir := updateFixture(t)

	// Before any panel save: no panel file, script source rules apply.
	writeFile(t, dir, autoupdateFileName, "ENABLED=1\nFREQ=weekly\nLAST_CHECK=2026-11-20 03:37:00\nLAST_RESULT=up-to-date v0.1.6\n")
	_, body := doReq(t, http.MethodGet, base+"/api/admin/update", testAdminToken, nil)
	au, _ := body["auto_update"].(map[string]any)
	if au["source"] != "script" || au["enabled"] != true || au["freq"] != "weekly" {
		t.Fatalf("script-sourced state wrong: %v", au)
	}

	// Saving the update keys writes the panel contract file.
	code, _ := putSettings(t, base, testAdminToken, map[string]*string{
		settings.KeyAutoUpdateEnabled: strp("true"),
		settings.KeyAutoUpdateTime:    strp("04:15"),
	})
	if code != http.StatusOK {
		t.Fatalf("PUT settings: got %d", code)
	}
	kv, ok := parseKVFile(filepath.Join(dir, updatePanelFileName))
	if !ok || kv["ENABLED"] != "1" || kv["TIME"] != "04:15" {
		t.Fatalf("panel file wrong: %v (ok=%v)", kv, ok)
	}

	// And the panel now wins over the CLI state.
	_, body = doReq(t, http.MethodGet, base+"/api/admin/update", testAdminToken, nil)
	au, _ = body["auto_update"].(map[string]any)
	if au["source"] != "panel" || au["enabled"] != true || au["time"] != "04:15" {
		t.Fatalf("panel-sourced state wrong: %v", au)
	}
	if au["last_result"] != "up-to-date v0.1.6" {
		t.Fatalf("last_result not carried from CLI state: %v", au)
	}

	// An unrelated save must not create/overwrite the panel file.
	if err := os.Remove(filepath.Join(dir, updatePanelFileName)); err != nil {
		t.Fatal(err)
	}
	code, _ = putSettings(t, base, testAdminToken, map[string]*string{
		settings.KeySiteName: strp("Renamed"),
	})
	if code != http.StatusOK {
		t.Fatalf("PUT unrelated settings: got %d", code)
	}
	if _, ok := parseKVFile(filepath.Join(dir, updatePanelFileName)); ok {
		t.Fatal("unrelated save created the panel file")
	}

	// Bad clock values are rejected by validation, atomically.
	code, body2 := putSettings(t, base, testAdminToken, map[string]*string{
		settings.KeyAutoUpdateTime: strp("25:00"),
	})
	if code != http.StatusBadRequest {
		t.Fatalf("PUT bad time: got %d (%v), want 400", code, body2)
	}
}

func TestValidClockTimeViaSettings(t *testing.T) {
	for _, bad := range []string{"3:37", "24:00", "03:60", "ab:cd", "03:37:00", ""} {
		if settings.ValidClockTime(bad) {
			t.Errorf("ValidClockTime(%q) = true, want false", bad)
		}
	}
	for _, good := range []string{"00:00", "03:37", "23:59"} {
		if !settings.ValidClockTime(good) {
			t.Errorf("ValidClockTime(%q) = false, want true", good)
		}
	}
}
