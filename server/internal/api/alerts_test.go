package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"dhtsearch/server/internal/alerts"
	"dhtsearch/server/internal/store"
)

// stubAlerts is a scripted AlertsService: Test returns the canned
// result, so the endpoint's status mapping is deterministic.
type stubAlerts struct {
	res alerts.TestResult
}

func (s *stubAlerts) Test(context.Context) alerts.TestResult { return s.res }

func alertsFixture(t *testing.T, svc AlertsService) string {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := New(st, nil, nil, Options{AdminToken: testAdminToken, Alerts: svc})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts.URL
}

func TestAlertsTestEndpoint(t *testing.T) {
	stub := &stubAlerts{res: alerts.TestResult{Channels: []alerts.ChannelResult{
		{Channel: "webhook", OK: true},
		{Channel: "telegram", OK: false, Error: "telegram: chat not found"},
	}}}
	base := alertsFixture(t, stub)

	// Unauthenticated callers never reach the probe.
	if code, _ := doReq(t, http.MethodPost, base+"/api/admin/alerts/test", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("test without token: got %d, want 401", code)
	}

	// Per-channel outcomes are reported in-band with a 200: a
	// failing channel is an answer, not a server error.
	code, body := doReq(t, http.MethodPost, base+"/api/admin/alerts/test", testAdminToken, nil)
	if code != http.StatusOK {
		t.Fatalf("test: got %d (%v), want 200", code, body)
	}
	channels, ok := body["channels"].([]any)
	if !ok || len(channels) != 2 {
		t.Fatalf("channels payload: %v", body)
	}
	first, _ := channels[0].(map[string]any)
	second, _ := channels[1].(map[string]any)
	if first["channel"] != "webhook" || first["ok"] != true {
		t.Fatalf("webhook result: %v", first)
	}
	if second["channel"] != "telegram" || second["ok"] != false || second["error"] != "telegram: chat not found" {
		t.Fatalf("telegram result: %v", second)
	}
}

// No configured channel is a clear 400, not a silent success.
func TestAlertsTestNoChannels(t *testing.T) {
	base := alertsFixture(t, &stubAlerts{res: alerts.TestResult{}})
	code, body := doReq(t, http.MethodPost, base+"/api/admin/alerts/test", testAdminToken, nil)
	if code != http.StatusBadRequest {
		t.Fatalf("test with no channels: got %d (%v), want 400", code, body)
	}
}

func TestAlertsTestUnavailable(t *testing.T) {
	base := alertsFixture(t, nil)
	if code, _ := doReq(t, http.MethodPost, base+"/api/admin/alerts/test", testAdminToken, nil); code != http.StatusServiceUnavailable {
		t.Fatalf("test without service: got %d, want 503", code)
	}
}

func TestAlertsTestReviewerForbidden(t *testing.T) {
	fx := newAuthFixture(t, "", nil)
	fx.addUser(t, "rev", "rev-password", store.RoleReviewer, false)
	_, login := fx.passwordLogin(t, "rev", "rev-password")
	session := sessionOf(t, login)
	if code, _ := doReq(t, http.MethodPost, fx.url+"/api/admin/alerts/test", session, nil); code != http.StatusForbidden {
		t.Fatalf("reviewer alerts test: got %d, want 403", code)
	}
}
