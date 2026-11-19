package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestIPLimiterRefillAndIsolation(t *testing.T) {
	l := newIPLimiter(1, 2) // 1 rps, burst 2
	now := time.Unix(1000, 0)
	l.now = func() time.Time { return now }

	if !l.allow("a") || !l.allow("a") {
		t.Fatal("burst of 2 should be allowed")
	}
	if l.allow("a") {
		t.Fatal("third immediate request should be rejected")
	}
	// A different client has its own bucket.
	if !l.allow("b") {
		t.Fatal("second client must not share the first client's bucket")
	}
	// One second refills one token.
	now = now.Add(time.Second)
	if !l.allow("a") {
		t.Fatal("token should have refilled after 1s")
	}
	if l.allow("a") {
		t.Fatal("refill must not exceed elapsed time * rate")
	}
}

func TestIPLimiterSweepsIdleBuckets(t *testing.T) {
	l := newIPLimiter(1, 1)
	now := time.Unix(1000, 0)
	l.now = func() time.Time { return now }

	l.allow("a")
	now = now.Add(bucketIdleTTL + sweepEvery)
	l.allow("b") // triggers the sweep
	l.mu.Lock()
	_, stale := l.buckets["a"]
	l.mu.Unlock()
	if stale {
		t.Fatal("idle bucket survived the sweep")
	}
}

func TestClientKey(t *testing.T) {
	req := func(remote, xff, realIP string) *http.Request {
		r := httptest.NewRequest("GET", "/api/search", nil)
		r.RemoteAddr = remote
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		if realIP != "" {
			r.Header.Set("X-Real-IP", realIP)
		}
		return r
	}
	cases := []struct {
		name            string
		remote, xff, xr string
		want            string
	}{
		// A public peer's forwarding headers are spoofable — ignored.
		{"public peer ignores xff", "203.0.113.7:1234", "198.51.100.1", "", "203.0.113.7"},
		// A trusted proxy vouches for the rightmost untrusted hop only.
		{"proxy forwards client", "127.0.0.1:80", "198.51.100.1", "", "198.51.100.1"},
		{"client-supplied hop ignored", "127.0.0.1:80", "1.2.3.4, 198.51.100.1", "", "198.51.100.1"},
		{"chain of trusted proxies", "127.0.0.1:80", "198.51.100.1, 10.0.0.5", "", "198.51.100.1"},
		{"garbage stops the walk", "127.0.0.1:80", "1.2.3.4, garbage, 10.0.0.5", "", "127.0.0.1"},
		{"x-real-ip fallback", "127.0.0.1:80", "", "198.51.100.1", "198.51.100.1"},
		{"no headers keys the peer", "127.0.0.1:80", "", "", "127.0.0.1"},
		// IPv6 clients are bucketed by /64.
		{"ipv6 collapsed to /64", "[2001:db8:1:2:3:4:5:6]:443", "", "", "2001:db8:1:2::/64"},
	}
	for _, c := range cases {
		if got := clientKey(req(c.remote, c.xff, c.xr)); got != c.want {
			t.Errorf("%s: clientKey = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestRateLimitedEndpoint(t *testing.T) {
	base := testServerOpts(t, Options{RateRPS: 0.001, RateBurst: 2})

	get := func(path string) int {
		t.Helper()
		resp, err := http.Get(base.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if got := get("/api/search?q="); got != http.StatusOK {
		t.Fatalf("first request: status %d", got)
	}
	if got := get("/api/search?q="); got != http.StatusOK {
		t.Fatalf("second request: status %d", got)
	}
	if got := get("/api/search?q="); got != http.StatusTooManyRequests {
		t.Fatalf("over-budget request: status %d, want 429", got)
	}
	// The health check must stay reachable during a flood.
	if got := get("/api/healthz"); got != http.StatusOK {
		t.Fatalf("healthz while throttled: status %d", got)
	}
}

func TestTruncateUTF8(t *testing.T) {
	// 3-byte runes; a naive byte cut at 200 would split one.
	s := ""
	for len(s) <= maxQueryLen {
		s += "宇"
	}
	got := truncateUTF8(s, maxQueryLen)
	if len(got) > maxQueryLen {
		t.Fatalf("truncated to %d bytes, want <= %d", len(got), maxQueryLen)
	}
	if len(got)%3 != 0 {
		t.Fatalf("cut split a rune: %d bytes", len(got))
	}
	if truncateUTF8("abc", 200) != "abc" {
		t.Fatal("short strings must pass through")
	}
}
func TestAdminExemptFromPublicLimiter(t *testing.T) {
	base := testServerOpts(t, Options{RateRPS: 0.001, RateBurst: 1, AdminToken: testAdminToken})

	get := func(path, token string) int {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, base.URL+path, nil)
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
		resp.Body.Close()
		return resp.StatusCode
	}

	// Drain the single-token bucket through the public API.
	if got := get("/api/search?q=", ""); got != http.StatusOK {
		t.Fatalf("first public request: status %d", got)
	}
	if got := get("/api/search?q=", ""); got != http.StatusTooManyRequests {
		t.Fatalf("second public request: status %d, want 429", got)
	}
	// Admin paths bypass the public limiter entirely, so an operator
	// can always reach the settings page that undoes a bad limit.
	for i := 0; i < 5; i++ {
		if got := get("/api/admin/overview", testAdminToken); got != http.StatusOK {
			t.Fatalf("admin overview #%d while public throttled: status %d, want 200", i, got)
		}
	}
	// Exemption is not an auth bypass.
	if got := get("/api/admin/overview", ""); got != http.StatusUnauthorized {
		t.Fatalf("admin overview without token: status %d, want 401", got)
	}
	// The public API is still throttled afterwards.
	if got := get("/api/search?q=", ""); got != http.StatusTooManyRequests {
		t.Fatalf("public request after admin calls: status %d, want 429", got)
	}
}
