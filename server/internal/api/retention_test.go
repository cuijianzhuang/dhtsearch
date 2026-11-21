package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"dhtsearch/server/internal/retention"
	"dhtsearch/server/internal/store"
)

// stubRetention is a deterministic RetentionService: Trigger's
// answer is scripted so the in-flight 409 does not depend on a
// real run's timing.
type stubRetention struct {
	status  retention.Status
	trigger bool
}

func (s *stubRetention) Status(context.Context) (retention.Status, error) {
	return s.status, nil
}

func (s *stubRetention) Trigger() bool { return s.trigger }

func retentionFixture(t *testing.T, svc RetentionService) string {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := New(st, nil, nil, Options{AdminToken: testAdminToken, Retention: svc})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts.URL
}

func TestRetentionEndpoints(t *testing.T) {
	stub := &stubRetention{
		status: retention.Status{
			Enabled: false, MinAgeDays: 30, IntervalH: 24,
			EligibleNow: 7, DeletedTotal: 42,
			LastRun: &retention.RunInfo{At: 1_700_000_000, Deleted: 5, EligibleRemaining: 2},
		},
		trigger: true,
	}
	base := retentionFixture(t, stub)

	// Unauthenticated callers never reach the retention endpoints.
	if code, _ := doReq(t, http.MethodGet, base+"/api/admin/retention", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("status without token: got %d, want 401", code)
	}
	if code, _ := doReq(t, http.MethodPost, base+"/api/admin/retention/run", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("run without token: got %d, want 401", code)
	}

	_, body := doReq(t, http.MethodGet, base+"/api/admin/retention", testAdminToken, nil)
	if body["enabled"] != false || body["min_age_days"] != float64(30) ||
		body["eligible_now"] != float64(7) || body["deleted_total"] != float64(42) ||
		body["running"] != false {
		t.Fatalf("status payload: %v", body)
	}
	last, ok := body["last_run"].(map[string]any)
	if !ok || last["deleted"] != float64(5) || last["eligible_remaining"] != float64(2) {
		t.Fatalf("last_run payload: %v", body["last_run"])
	}

	code, _ := doReq(t, http.MethodPost, base+"/api/admin/retention/run", testAdminToken, nil)
	if code != http.StatusAccepted {
		t.Fatalf("run: got %d, want 202", code)
	}

	// A run already in flight conflicts.
	stub.trigger = false
	code, body = doReq(t, http.MethodPost, base+"/api/admin/retention/run", testAdminToken, nil)
	if code != http.StatusConflict {
		t.Fatalf("run while in flight: got %d (%v), want 409", code, body)
	}
}

func TestRetentionUnavailable(t *testing.T) {
	base := retentionFixture(t, nil)
	if code, _ := doReq(t, http.MethodGet, base+"/api/admin/retention", testAdminToken, nil); code != http.StatusServiceUnavailable {
		t.Fatalf("status without runner: got %d, want 503", code)
	}
	if code, _ := doReq(t, http.MethodPost, base+"/api/admin/retention/run", testAdminToken, nil); code != http.StatusServiceUnavailable {
		t.Fatalf("run without runner: got %d, want 503", code)
	}
}

func TestRetentionReviewerForbidden(t *testing.T) {
	fx := newAuthFixture(t, "", nil)
	fx.addUser(t, "rev", "rev-password", store.RoleReviewer, false)
	_, login := fx.passwordLogin(t, "rev", "rev-password")
	session := sessionOf(t, login)
	if code, _ := doReq(t, http.MethodGet, fx.url+"/api/admin/retention", session, nil); code != http.StatusForbidden {
		t.Fatalf("reviewer GET retention: got %d, want 403", code)
	}
	if code, _ := doReq(t, http.MethodPost, fx.url+"/api/admin/retention/run", session, nil); code != http.StatusForbidden {
		t.Fatalf("reviewer POST retention/run: got %d, want 403", code)
	}
}
