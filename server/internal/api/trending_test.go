package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"dhtsearch/server/internal/store"
)

// stubTrendingRefresh is a deterministic TrendingRefresher: the
// TriggerRefresh answer is scripted so the already-queued 409 does
// not depend on a real fetch cycle's timing.
type stubTrendingRefresh struct{ trigger bool }

func (s *stubTrendingRefresh) TriggerRefresh() bool { return s.trigger }

func trendingFixture(t *testing.T, svc TrendingRefresher) string {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := New(st, nil, nil, Options{
		AdminToken:      testAdminToken,
		TrendingRefresh: svc,
		TrendingStatus: func() TrendingStatus {
			return TrendingStatus{Failing: true, FailStreak: 3, LastSuccess: 1_700_000_000}
		},
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts.URL
}

func TestTrendingRefreshEndpoint(t *testing.T) {
	stub := &stubTrendingRefresh{trigger: true}
	base := trendingFixture(t, stub)

	// Unauthenticated callers never reach the endpoint.
	if code, _ := doReq(t, http.MethodPost, base+"/api/admin/trending/refresh", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("refresh without token: got %d, want 401", code)
	}

	code, body := doReq(t, http.MethodPost, base+"/api/admin/trending/refresh", testAdminToken, nil)
	if code != http.StatusAccepted {
		t.Fatalf("refresh: got %d (%v), want 202", code, body)
	}
	if body["accepted"] != true || body["failing"] != true ||
		body["fail_streak"] != float64(3) || body["last_success"] != float64(1_700_000_000) {
		t.Fatalf("refresh payload: %v", body)
	}

	// A refresh already queued conflicts instead of stacking.
	stub.trigger = false
	code, _ = doReq(t, http.MethodPost, base+"/api/admin/trending/refresh", testAdminToken, nil)
	if code != http.StatusConflict {
		t.Fatalf("refresh while queued: got %d, want 409", code)
	}
}

func TestTrendingRefreshUnavailable(t *testing.T) {
	base := trendingFixture(t, nil)
	if code, _ := doReq(t, http.MethodPost, base+"/api/admin/trending/refresh", testAdminToken, nil); code != http.StatusServiceUnavailable {
		t.Fatalf("refresh without service: got %d, want 503", code)
	}
}

func TestTrendingRefreshReviewerForbidden(t *testing.T) {
	fx := newAuthFixture(t, "", nil)
	fx.addUser(t, "rev", "rev-password", store.RoleReviewer, false)
	_, login := fx.passwordLogin(t, "rev", "rev-password")
	session := sessionOf(t, login)
	if code, _ := doReq(t, http.MethodPost, fx.url+"/api/admin/trending/refresh", session, nil); code != http.StatusForbidden {
		t.Fatalf("reviewer POST trending/refresh: got %d, want 403", code)
	}
}
