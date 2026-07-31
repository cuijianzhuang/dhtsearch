package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"dhtsearch/server/internal/settings"
	"dhtsearch/server/internal/store"
)

const testPassword = "correct horse battery staple"

type fakeSettingsStore struct{ vals map[string]string }

func (f *fakeSettingsStore) Settings() (map[string]string, error) { return f.vals, nil }
func (f *fakeSettingsStore) SetSetting(k, v string) error         { f.vals[k] = v; return nil }

func newConsole(t *testing.T, tweak func(*Config)) (*httptest.Server, *store.Store, *settings.Settings) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	live, err := settings.Load(&fakeSettingsStore{vals: map[string]string{}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		Password: testPassword,
		Settings: live,
		Stats: func(context.Context) ([]byte, error) {
			return []byte(`{"torrents":7}`), nil
		},
		Logger: log.New(io.Discard, "", 0),
	}
	if tweak != nil {
		tweak(&cfg)
	}
	s := New(st, cfg)
	if s == nil && cfg.Password != "" {
		t.Fatal("New returned nil despite a password")
	}
	mux := http.NewServeMux()
	if s != nil {
		s.Mount(mux)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, st, live
}

// client keeps cookies, like a browser.
func newClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Jar: jar}
}

func post(t *testing.T, c *http.Client, url, body string, hdr map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// login authenticates and returns the CSRF token the page would read.
func login(t *testing.T, c *http.Client, srv *httptest.Server) string {
	t.Helper()
	resp := post(t, c, srv.URL+"/api/admin/login",
		`{"password":`+quote(testPassword)+`}`, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login: status %d", resp.StatusCode)
	}
	for _, ck := range resp.Cookies() {
		if ck.Name == csrfCookie {
			return ck.Value
		}
	}
	t.Fatal("login did not set a CSRF cookie")
	return ""
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// No password means no console at all — a console reachable without one would
// be strictly worse than not having it.
func TestDisabledWithoutPassword(t *testing.T) {
	if s := New(nil, Config{Password: ""}); s != nil {
		t.Fatal("New returned a server with no password configured")
	}
}

func TestUnauthenticatedRequestsAreRejected(t *testing.T) {
	srv, _, _ := newConsole(t, nil)
	c := newClient(t)

	for _, path := range []string{"/api/admin/state", "/api/admin/blocked"} {
		resp, err := c.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET %s: status %d, want 401", path, resp.StatusCode)
		}
	}
	for _, path := range []string{
		"/api/admin/settings", "/api/admin/torrent/delete", "/api/admin/unblock",
		"/api/admin/unblock-reason", "/api/admin/reset-reviewed", "/api/admin/sweep",
	} {
		resp := post(t, c, srv.URL+path, `{}`, nil)
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("POST %s: status %d, want 401", path, resp.StatusCode)
		}
	}
}

func TestLoginAndSessionLifecycle(t *testing.T) {
	srv, _, _ := newConsole(t, nil)
	c := newClient(t)

	bad := post(t, c, srv.URL+"/api/admin/login", `{"password":"wrong"}`, nil)
	bad.Body.Close()
	if bad.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong password: status %d, want 401", bad.StatusCode)
	}

	csrf := login(t, c, srv)
	resp, err := c.Get(srv.URL + "/api/admin/state")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("state after login: status %d", resp.StatusCode)
	}
	var state struct {
		Stats    json.RawMessage `json:"stats"`
		Settings []settings.Def  `json:"settings"`
		CanSweep bool            `json:"can_sweep"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(state.Stats, []byte(`"torrents":7`)) {
		t.Errorf("stats not passed through: %s", state.Stats)
	}
	if len(state.Settings) == 0 {
		t.Error("no settings returned")
	}
	if state.CanSweep {
		t.Error("can_sweep true with no SweepNow hook configured")
	}

	// Logging out must invalidate the session server-side, not just clear the
	// cookie: a token someone copied has to stop working.
	out := post(t, c, srv.URL+"/api/admin/logout", `{}`, map[string]string{csrfHeader: csrf})
	out.Body.Close()
	after := post(t, c, srv.URL+"/api/admin/settings",
		`{"key":"filter_adult","value":"false"}`, map[string]string{csrfHeader: csrf})
	after.Body.Close()
	if after.StatusCode != http.StatusUnauthorized {
		t.Errorf("request after logout: status %d, want 401", after.StatusCode)
	}
}

// SameSite=Strict is the primary CSRF defence, but a browser that ignores it
// still must not be able to drive the console: a cross-site form post cannot
// set a custom header.
func TestMutationsRequireCSRFHeader(t *testing.T) {
	srv, _, live := newConsole(t, nil)
	c := newClient(t)
	csrf := login(t, c, srv)

	body := `{"key":"filter_adult","value":"false"}`
	for _, tc := range []struct {
		name string
		hdr  map[string]string
	}{
		{"no header", nil},
		{"wrong token", map[string]string{csrfHeader: "not-the-token"}},
		{"empty token", map[string]string{csrfHeader: ""}},
	} {
		resp := post(t, c, srv.URL+"/api/admin/settings", body, tc.hdr)
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401", tc.name, resp.StatusCode)
		}
	}
	if !live.Bool(settings.FilterAdult) {
		t.Fatal("a rejected request still changed the setting")
	}

	ok := post(t, c, srv.URL+"/api/admin/settings", body, map[string]string{csrfHeader: csrf})
	ok.Body.Close()
	if ok.StatusCode != http.StatusOK {
		t.Fatalf("with the CSRF header: status %d", ok.StatusCode)
	}
	if live.Bool(settings.FilterAdult) {
		t.Error("setting not applied")
	}
}

// A shared password on the public internet is exactly what gets guessed, so
// repeated failures have to stop being answered.
func TestLoginLockout(t *testing.T) {
	srv, _, _ := newConsole(t, nil)
	c := newClient(t)

	for i := 0; i < maxLoginFails; i++ {
		resp := post(t, c, srv.URL+"/api/admin/login", `{"password":"wrong"}`, nil)
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status %d, want 401", i+1, resp.StatusCode)
		}
	}
	locked := post(t, c, srv.URL+"/api/admin/login", `{"password":"wrong"}`, nil)
	locked.Body.Close()
	if locked.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("after %d failures: status %d, want 429", maxLoginFails, locked.StatusCode)
	}
	if locked.Header.Get("Retry-After") == "" {
		t.Error("no Retry-After on a lockout")
	}
	// The lockout must hold even for the right password, or it is no defence:
	// an attacker's guess would simply be waved through when it lands.
	right := post(t, c, srv.URL+"/api/admin/login", `{"password":`+quote(testPassword)+`}`, nil)
	right.Body.Close()
	if right.StatusCode != http.StatusTooManyRequests {
		t.Errorf("correct password during lockout: status %d, want 429", right.StatusCode)
	}
}

func TestAdminOperations(t *testing.T) {
	srv, st, _ := newConsole(t, func(c *Config) {
		c.SweepNow = func(context.Context) (int, int64, error) { return 3, 1, nil }
	})
	c := newClient(t)
	csrf := login(t, c, srv)
	hdr := map[string]string{csrfHeader: csrf}
	hash := strings.Repeat("a", 40)

	if err := st.Upsert(store.Torrent{
		InfoHash: hash, Name: "Something 1080p", TotalSize: 1 << 30, CreatedAt: 100,
	}); err != nil {
		t.Fatal(err)
	}

	// Delete with blocklisting: the row goes and the hash cannot come back.
	resp := post(t, c, srv.URL+"/api/admin/torrent/delete",
		`{"info_hash":`+quote(hash)+`,"blocklist":true}`, hdr)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete: status %d", resp.StatusCode)
	}
	if known, _ := st.Known(context.Background(), hash); !known {
		t.Error("deleted hash should still read as known via the blocklist")
	}
	if n, _ := st.BlockedCount(context.Background()); n != 1 {
		t.Errorf("blocked count = %d, want 1", n)
	}

	// Unblocking lifts it again.
	resp = post(t, c, srv.URL+"/api/admin/unblock", `{"info_hash":`+quote(hash)+`}`, hdr)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unblock: status %d", resp.StatusCode)
	}
	if n, _ := st.BlockedCount(context.Background()); n != 0 {
		t.Errorf("blocked count after unblock = %d, want 0", n)
	}

	// A manual sweep reports what the pass did.
	resp = post(t, c, srv.URL+"/api/admin/sweep", `{}`, hdr)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sweep: status %d", resp.StatusCode)
	}
	var sw struct {
		Reviewed int   `json:"reviewed"`
		Deleted  int64 `json:"deleted"`
	}
	json.NewDecoder(resp.Body).Decode(&sw)
	if sw.Reviewed != 3 || sw.Deleted != 1 {
		t.Errorf("sweep reported %+v, want reviewed=3 deleted=1", sw)
	}
}

// Malformed input must be refused before it reaches the store, so a typo
// cannot blocklist a garbage key that then sits in the table forever.
func TestOperationsValidateInput(t *testing.T) {
	srv, _, _ := newConsole(t, nil)
	c := newClient(t)
	hdr := map[string]string{csrfHeader: login(t, c, srv)}

	for _, tc := range []struct{ path, body string }{
		{"/api/admin/torrent/delete", `{"info_hash":"nope"}`},
		{"/api/admin/torrent/delete", `{"info_hash":""}`},
		{"/api/admin/unblock", `{"info_hash":"tooshort"}`},
		{"/api/admin/unblock-reason", `{"reason":"whatever"}`},
		{"/api/admin/settings", `{"key":"made_up","value":"1"}`},
		{"/api/admin/settings", `{"key":"min_torrent_size","value":"-5"}`},
	} {
		resp := post(t, c, srv.URL+tc.path, tc.body, hdr)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("POST %s %s: status %d, want 400", tc.path, tc.body, resp.StatusCode)
		}
	}
}

// The sweep button must say so rather than appearing to work when moderation
// is not running at all.
func TestSweepUnavailableWithoutModeration(t *testing.T) {
	srv, _, _ := newConsole(t, nil)
	c := newClient(t)
	hdr := map[string]string{csrfHeader: login(t, c, srv)}
	resp := post(t, c, srv.URL+"/api/admin/sweep", `{}`, hdr)
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("sweep without moderation: status %d, want 409", resp.StatusCode)
	}
}

// The console page must not be cacheable or embeddable, and must carry a CSP.
func TestConsolePageHeaders(t *testing.T) {
	srv, _, _ := newConsole(t, nil)
	resp, err := http.Get(srv.URL + "/admin")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte("DHTSearch")) {
		t.Error("console page did not render")
	}
	for h, want := range map[string]string{
		"Cache-Control":          "no-store",
		"X-Content-Type-Options": "nosniff",
	} {
		if got := resp.Header.Get(h); got != want {
			t.Errorf("%s = %q, want %q", h, got, want)
		}
	}
	csp := resp.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("CSP missing frame-ancestors: %q", csp)
	}
	if !strings.Contains(csp, "default-src 'none'") {
		t.Errorf("CSP missing default-src: %q", csp)
	}
}

// Session cookies must be HttpOnly and SameSite=Strict; the CSRF cookie must
// be readable by script, since the page has to echo it back in a header.
func TestCookieAttributes(t *testing.T) {
	srv, _, _ := newConsole(t, func(c *Config) { c.Secure = true })
	c := newClient(t)
	resp := post(t, c, srv.URL+"/api/admin/login", `{"password":`+quote(testPassword)+`}`, nil)
	defer resp.Body.Close()

	var seenSession, seenCSRF bool
	for _, ck := range resp.Cookies() {
		switch ck.Name {
		case sessionCookie:
			seenSession = true
			if !ck.HttpOnly {
				t.Error("session cookie is not HttpOnly")
			}
			if !ck.Secure {
				t.Error("session cookie is not Secure despite Secure config")
			}
			if ck.SameSite != http.SameSiteStrictMode {
				t.Errorf("session cookie SameSite = %v, want Strict", ck.SameSite)
			}
		case csrfCookie:
			seenCSRF = true
			if ck.HttpOnly {
				t.Error("CSRF cookie is HttpOnly; the page cannot read it")
			}
		}
	}
	if !seenSession || !seenCSRF {
		t.Fatalf("cookies set: session=%v csrf=%v", seenSession, seenCSRF)
	}
}

// A forged X-Forwarded-For from a public peer must not let an attacker spread
// login attempts across identities to dodge the lockout.
func TestClientIPIgnoresUntrustedForwardedHeaders(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/admin/login", nil)
	r.RemoteAddr = "203.0.113.9:5555"
	r.Header.Set("X-Forwarded-For", "1.2.3.4")
	if got := clientIP(r); got != "203.0.113.9" {
		t.Errorf("public peer: clientIP = %q, want the real peer address", got)
	}

	// Behind the reverse proxy the header is the only way to see the visitor.
	r2 := httptest.NewRequest(http.MethodPost, "/api/admin/login", nil)
	r2.RemoteAddr = "127.0.0.1:5555"
	r2.Header.Set("X-Forwarded-For", "198.51.100.7")
	if got := clientIP(r2); got != "198.51.100.7" {
		t.Errorf("trusted peer: clientIP = %q, want the forwarded address", got)
	}
}

func TestSessionTokensAreDistinct(t *testing.T) {
	a := newAuth("pw", false)
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		tok, csrf := a.startSession("1.2.3.4")
		if tok == csrf {
			t.Fatal("session and CSRF tokens are identical")
		}
		if seen[tok] {
			t.Fatal("duplicate session token")
		}
		seen[tok] = true
	}
}

func TestExpiredSessionIsRejected(t *testing.T) {
	a := newAuth("pw", false)
	now := time.Now()
	a.now = func() time.Time { return now }
	tok, _ := a.startSession("1.2.3.4")
	if _, ok := a.lookup(tok); !ok {
		t.Fatal("fresh session not found")
	}
	now = now.Add(sessionTTL + time.Minute)
	if _, ok := a.lookup(tok); ok {
		t.Error("expired session still accepted")
	}
}
