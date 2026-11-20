package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dhtsearch/server/internal/store"
)

func TestRestartRequestLifecycle(t *testing.T) {
	_, base, dir := updateFixture(t)

	// Unauthenticated callers never reach the restart endpoints.
	if code, _ := doReq(t, http.MethodPost, base+"/api/admin/restart", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("request without token: got %d, want 401", code)
	}
	if code, _ := doReq(t, http.MethodGet, base+"/api/admin/restart", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("status without token: got %d, want 401", code)
	}

	// Idle before anything was ever requested.
	_, body := doReq(t, http.MethodGet, base+"/api/admin/restart", testAdminToken, nil)
	if body["state"] != "idle" || body["pending"] != false || body["contract_ready"] != true {
		t.Fatalf("initial state: %v", body)
	}

	code, body := doReq(t, http.MethodPost, base+"/api/admin/restart", testAdminToken, nil)
	if code != http.StatusAccepted {
		t.Fatalf("request: got %d (%v), want 202", code, body)
	}
	if body["state"] != "requested" || body["pending"] != true {
		t.Fatalf("state after request: %v", body)
	}
	reqKV, ok := parseKVFile(filepath.Join(dir, restartRequestFileName))
	if !ok || reqKV["REQUEST_ID"] == "" || reqKV["REQUESTED_BY"] == "" {
		t.Fatalf("request flag not written: %v", reqKV)
	}
	if body["request_id"] != reqKV["REQUEST_ID"] {
		t.Fatalf("payload id %v != flag id %v", body["request_id"], reqKV["REQUEST_ID"])
	}

	// A second request while one is pending (and fresh) conflicts.
	code, _ = doReq(t, http.MethodPost, base+"/api/admin/restart", testAdminToken, nil)
	if code != http.StatusConflict {
		t.Fatalf("duplicate request: got %d, want 409", code)
	}

	// The agent consumes the flag and records the outcome.
	writeFile(t, dir, restartStatusFileName, strings.Join([]string{
		"STATE=succeeded",
		"REQUEST_ID=" + reqKV["REQUEST_ID"],
		"REQUESTED_AT=" + reqKV["REQUESTED_AT"],
		"REQUESTED_BY=" + reqKV["REQUESTED_BY"],
		"STARTED_AT=2026-11-20 10:00:05",
		"FINISHED_AT=2026-11-20 10:00:12",
		"MESSAGE=service restarted; API is answering again",
		"",
	}, "\n"))
	if err := os.Remove(filepath.Join(dir, restartRequestFileName)); err != nil {
		t.Fatal(err)
	}
	_, body = doReq(t, http.MethodGet, base+"/api/admin/restart", testAdminToken, nil)
	if body["state"] != "succeeded" || body["pending"] != false || body["finished_at"] == "" {
		t.Fatalf("state after agent run: %v", body)
	}

	// After the outcome is recorded, a new restart may be requested.
	code, _ = doReq(t, http.MethodPost, base+"/api/admin/restart", testAdminToken, nil)
	if code != http.StatusAccepted {
		t.Fatalf("request after success: got %d, want 202", code)
	}

	// While the agent reports a restart in flight, requests conflict
	// (and the state reads pending).
	writeFile(t, dir, restartStatusFileName, "STATE=running\nREQUEST_ID=zzz\n")
	code, _ = doReq(t, http.MethodPost, base+"/api/admin/restart", testAdminToken, nil)
	if code != http.StatusConflict {
		t.Fatalf("request during run: got %d, want 409", code)
	}
	_, body = doReq(t, http.MethodGet, base+"/api/admin/restart", testAdminToken, nil)
	if body["state"] != "running" || body["pending"] != true {
		t.Fatalf("running state: %v", body)
	}
}

func TestRestartStaleRequestDoesNotBlock(t *testing.T) {
	_, base, dir := updateFixture(t)

	// A request flag the agent never consumed (an installer too old
	// for the restart contract, a stopped timer) must not block the
	// panel forever: once it is older than the freshness window a
	// new request replaces it.
	writeFile(t, dir, restartRequestFileName, "REQUEST_ID=abandoned\nREQUESTED_AT=2026-11-20 09:00:00\nREQUESTED_BY=admin\n")
	old := time.Now().Add(-restartRequestFreshness - time.Minute)
	if err := os.Chtimes(filepath.Join(dir, restartRequestFileName), old, old); err != nil {
		t.Fatal(err)
	}
	code, body := doReq(t, http.MethodPost, base+"/api/admin/restart", testAdminToken, nil)
	if code != http.StatusAccepted {
		t.Fatalf("request over stale flag: got %d (%v), want 202", code, body)
	}
	reqKV, _ := parseKVFile(filepath.Join(dir, restartRequestFileName))
	if reqKV["REQUEST_ID"] == "abandoned" || reqKV["REQUEST_ID"] == "" {
		t.Fatalf("stale flag not replaced: %v", reqKV)
	}
}

func TestRestartReviewerForbidden(t *testing.T) {
	fx := newAuthFixture(t, "", nil)
	fx.addUser(t, "rev", "rev-password", store.RoleReviewer, false)
	_, login := fx.passwordLogin(t, "rev", "rev-password")
	session := sessionOf(t, login)
	if code, _ := doReq(t, http.MethodGet, fx.url+"/api/admin/restart", session, nil); code != http.StatusForbidden {
		t.Fatalf("reviewer GET restart: got %d, want 403", code)
	}
	if code, _ := doReq(t, http.MethodPost, fx.url+"/api/admin/restart", session, nil); code != http.StatusForbidden {
		t.Fatalf("reviewer POST restart: got %d, want 403", code)
	}
}

func TestRestartNoDataDir(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := New(st, nil, nil, Options{AdminToken: testAdminToken})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	base := ts.URL

	// Without a data dir there is no intent-file channel: requests
	// answer 503 and the status reports the contract as unavailable.
	code, body := doReq(t, http.MethodPost, base+"/api/admin/restart", testAdminToken, nil)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("no data dir: got %d (%v), want 503", code, body)
	}
	_, body = doReq(t, http.MethodGet, base+"/api/admin/restart", testAdminToken, nil)
	if body["contract_ready"] != false || body["state"] != "idle" {
		t.Fatalf("no-data-dir status: %v", body)
	}
}
