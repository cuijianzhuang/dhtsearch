package api

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"dhtsearch/server/internal/settings"
	"dhtsearch/server/internal/store"
)

// authFixture wires a Server over a memory store with a settings
// manager whose base layer is the given env-style values.
type authFixture struct {
	url string
	st  *store.Store
	mgr *settings.Manager
	srv *Server
}

func newAuthFixture(t *testing.T, token string, base map[string]string) *authFixture {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	mgr, err := settings.New(base, nil, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, nil, nil, Options{AdminToken: token, Settings: mgr})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return &authFixture{url: ts.URL, st: st, mgr: mgr, srv: s}
}

// addUser inserts an account directly (fast bcrypt: verification
// cost is independent of the cost the hash was made with).
func (f *authFixture) addUser(t *testing.T, username, password, role string, disabled bool) *store.AdminUser {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	u := &store.AdminUser{
		Username: username, PasswordHash: string(hash), Role: role,
		Source: store.SourceLocal, Disabled: disabled,
	}
	if err := f.st.CreateAdminUser(context.Background(), u, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	return u
}

func doReq(t *testing.T, method, url, bearer string, body any) (int, map[string]any) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		t.Fatal(err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (f *authFixture) passwordLogin(t *testing.T, username, password string) (int, map[string]any) {
	t.Helper()
	return doReq(t, http.MethodPost, f.url+"/api/admin/auth/password", "",
		map[string]string{"username": username, "password": password})
}

func sessionOf(t *testing.T, body map[string]any) string {
	t.Helper()
	s, _ := body["session"].(string)
	if s == "" {
		t.Fatalf("response has no session: %v", body)
	}
	return s
}

func TestPasswordLoginFlow(t *testing.T) {
	fx := newAuthFixture(t, "", nil) // no recovery token at all
	fx.addUser(t, "alice", "alice-password", store.RoleAdmin, false)

	status, body := fx.passwordLogin(t, "alice", "wrong-password")
	if status != http.StatusUnauthorized {
		t.Fatalf("wrong password: status %d, want 401 (%v)", status, body)
	}
	status, body = fx.passwordLogin(t, "nosuchuser", "alice-password")
	if status != http.StatusUnauthorized {
		t.Fatalf("unknown user: status %d, want 401 (same as wrong password)", status)
	}
	status, body = fx.passwordLogin(t, "alice", "alice-password")
	if status != http.StatusOK {
		t.Fatalf("good login: status %d (%v)", status, body)
	}
	session := sessionOf(t, body)

	status, me := doReq(t, http.MethodGet, fx.url+"/api/admin/me", session, nil)
	if status != http.StatusOK || me["username"] != "alice" || me["role"] != "admin" || me["method"] != "password" {
		t.Fatalf("me = %d %v", status, me)
	}
	status, _ = doReq(t, http.MethodGet, fx.url+"/api/admin/overview", session, nil)
	if status != http.StatusOK {
		t.Fatalf("overview with session: status %d", status)
	}
	// Last login got stamped.
	u, err := fx.st.AdminUserByUsername(context.Background(), "alice")
	if err != nil || u == nil || u.LastLoginAt == 0 {
		t.Fatalf("last login not stamped: %+v, %v", u, err)
	}
}

func TestPasswordLoginDisabledUser(t *testing.T) {
	fx := newAuthFixture(t, "", nil)
	fx.addUser(t, "mallory", "mallory-password", store.RoleAdmin, true)
	status, _ := fx.passwordLogin(t, "mallory", "mallory-password")
	if status != http.StatusUnauthorized {
		t.Fatalf("disabled user login: status %d, want 401", status)
	}
}

func TestReviewerRoleGating(t *testing.T) {
	fx := newAuthFixture(t, "", nil)
	fx.addUser(t, "root", "root-password", store.RoleAdmin, false)
	fx.addUser(t, "rev", "rev-password", store.RoleReviewer, false)

	_, body := fx.passwordLogin(t, "rev", "rev-password")
	revSession := sessionOf(t, body)

	// Moderation surfaces are fine for a reviewer...
	for _, path := range []string{"/api/admin/overview", "/api/admin/torrents", "/api/admin/blocked"} {
		if status, _ := doReq(t, http.MethodGet, fx.url+path, revSession, nil); status != http.StatusOK {
			t.Fatalf("reviewer GET %s: status %d, want 200", path, status)
		}
	}
	// ...settings and account management are not.
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/admin/settings"},
		{http.MethodPut, "/api/admin/settings"},
		{http.MethodGet, "/api/admin/users"},
		{http.MethodPost, "/api/admin/users"},
	} {
		if status, _ := doReq(t, tc.method, fx.url+tc.path, revSession, nil); status != http.StatusForbidden {
			t.Fatalf("reviewer %s %s: status %d, want 403", tc.method, tc.path, status)
		}
	}

	_, body = fx.passwordLogin(t, "root", "root-password")
	adminSession := sessionOf(t, body)
	if status, _ := doReq(t, http.MethodGet, fx.url+"/api/admin/settings", adminSession, nil); status != http.StatusOK {
		t.Fatalf("admin GET settings: status %d, want 200", status)
	}
	if status, _ := doReq(t, http.MethodGet, fx.url+"/api/admin/users", adminSession, nil); status != http.StatusOK {
		t.Fatalf("admin GET users: status %d, want 200", status)
	}
}

func TestTokenLoginSessionAndLogout(t *testing.T) {
	fx := newAuthFixture(t, testAdminToken, nil)

	status, body := doReq(t, http.MethodPost, fx.url+"/api/admin/login", "",
		map[string]string{"token": testAdminToken})
	if status != http.StatusOK {
		t.Fatalf("token login: status %d (%v)", status, body)
	}
	session := sessionOf(t, body)
	if session == testAdminToken {
		t.Fatal("session token must not be the recovery token itself")
	}
	if status, _ := doReq(t, http.MethodGet, fx.url+"/api/admin/overview", session, nil); status != http.StatusOK {
		t.Fatalf("overview with minted session: status %d", status)
	}
	// Logout revokes the session server-side...
	if status, _ := doReq(t, http.MethodPost, fx.url+"/api/admin/logout", session, nil); status != http.StatusOK {
		t.Fatalf("logout: status %d", status)
	}
	if status, _ := doReq(t, http.MethodGet, fx.url+"/api/admin/overview", session, nil); status != http.StatusUnauthorized {
		t.Fatalf("overview after logout: status %d, want 401", status)
	}
	// ...while the raw recovery token keeps working as the fallback.
	if status, _ := doReq(t, http.MethodGet, fx.url+"/api/admin/overview", testAdminToken, nil); status != http.StatusOK {
		t.Fatalf("overview with raw token: status %d", status)
	}
}

func TestPasswordChangeKicksOtherSessions(t *testing.T) {
	fx := newAuthFixture(t, "", nil)
	fx.addUser(t, "carol", "carol-old-password", store.RoleAdmin, false)

	_, b1 := fx.passwordLogin(t, "carol", "carol-old-password")
	s1 := sessionOf(t, b1)
	_, b2 := fx.passwordLogin(t, "carol", "carol-old-password")
	s2 := sessionOf(t, b2)

	status, body := doReq(t, http.MethodPost, fx.url+"/api/admin/me/password", s1,
		map[string]string{"current_password": "carol-old-password", "new_password": "carol-new-password"})
	if status != http.StatusOK {
		t.Fatalf("change password: status %d (%v)", status, body)
	}
	if status, _ := doReq(t, http.MethodGet, fx.url+"/api/admin/me", s2, nil); status != http.StatusUnauthorized {
		t.Fatalf("other session after change: status %d, want 401", status)
	}
	if status, _ := doReq(t, http.MethodGet, fx.url+"/api/admin/me", s1, nil); status != http.StatusOK {
		t.Fatalf("own session after change: status %d, want 200", status)
	}
	if status, _ := fx.passwordLogin(t, "carol", "carol-old-password"); status != http.StatusUnauthorized {
		t.Fatalf("old password login: status %d, want 401", status)
	}
	if status, _ := fx.passwordLogin(t, "carol", "carol-new-password"); status != http.StatusOK {
		t.Fatalf("new password login: status %d, want 200", status)
	}
	// Wrong current password is rejected.
	if status, _ := doReq(t, http.MethodPost, fx.url+"/api/admin/me/password", s1,
		map[string]string{"current_password": "nope-nope", "new_password": "whatever-123"}); status != http.StatusUnauthorized {
		t.Fatalf("change with wrong current: status %d, want 401", status)
	}
}

func TestUserManagementAndLastAdminGuard(t *testing.T) {
	fx := newAuthFixture(t, testAdminToken, nil)

	// Create the first admin through the token principal.
	status, body := doReq(t, http.MethodPost, fx.url+"/api/admin/users", testAdminToken,
		map[string]string{"username": "dave", "password": "dave-password", "role": "admin"})
	if status != http.StatusOK {
		t.Fatalf("create user: status %d (%v)", status, body)
	}
	daveID, _ := body["id"].(string)
	if daveID == "" {
		t.Fatalf("create user response: %v", body)
	}
	// Duplicate username conflicts.
	if status, _ := doReq(t, http.MethodPost, fx.url+"/api/admin/users", testAdminToken,
		map[string]string{"username": "dave", "password": "dave-password", "role": "admin"}); status != http.StatusConflict {
		t.Fatalf("duplicate create: status %d, want 409", status)
	}
	// The last enabled admin cannot be disabled, demoted or deleted.
	if status, body := doReq(t, http.MethodPost, fx.url+"/api/admin/users/"+daveID, testAdminToken,
		map[string]any{"disabled": true}); status != http.StatusBadRequest {
		t.Fatalf("disable last admin: status %d (%v), want 400", status, body)
	}
	if status, _ := doReq(t, http.MethodPost, fx.url+"/api/admin/users/"+daveID, testAdminToken,
		map[string]any{"role": "reviewer"}); status != http.StatusBadRequest {
		t.Fatalf("demote last admin: status %d, want 400", status)
	}
	if status, _ := doReq(t, http.MethodDelete, fx.url+"/api/admin/users/"+daveID, testAdminToken, nil); status != http.StatusBadRequest {
		t.Fatalf("delete last admin: status %d, want 400", status)
	}
	// With a second admin in place, disabling dave works and kills
	// his session.
	fx.addUser(t, "erin", "erin-password", store.RoleAdmin, false)
	_, lb := fx.passwordLogin(t, "dave", "dave-password")
	daveSession := sessionOf(t, lb)
	if status, _ := doReq(t, http.MethodPost, fx.url+"/api/admin/users/"+daveID, testAdminToken,
		map[string]any{"disabled": true}); status != http.StatusOK {
		t.Fatalf("disable with second admin: status %d", status)
	}
	if status, _ := doReq(t, http.MethodGet, fx.url+"/api/admin/me", daveSession, nil); status != http.StatusUnauthorized {
		t.Fatalf("disabled user's session: status %d, want 401", status)
	}
	// Admin reset of a local password works and retires sessions.
	_, lb = fx.passwordLogin(t, "erin", "erin-password")
	erinSession := sessionOf(t, lb)
	erin, _ := fx.st.AdminUserByUsername(context.Background(), "erin")
	if status, _ := doReq(t, http.MethodPost, fx.url+"/api/admin/users/"+erin.ID+"/password", testAdminToken,
		map[string]string{"password": "erin-new-password"}); status != http.StatusOK {
		t.Fatalf("reset password: status %d", status)
	}
	if status, _ := doReq(t, http.MethodGet, fx.url+"/api/admin/me", erinSession, nil); status != http.StatusUnauthorized {
		t.Fatalf("session after admin reset: status %d, want 401", status)
	}
}

func TestAdminSurfaceOffWithoutAnyLoginMethod(t *testing.T) {
	fx := newAuthFixture(t, "", nil)
	if status, _ := doReq(t, http.MethodGet, fx.url+"/api/admin/overview", "", nil); status != http.StatusServiceUnavailable {
		t.Fatalf("overview: status %d, want 503", status)
	}
	if status, _ := fx.passwordLogin(t, "alice", "whatever-123"); status != http.StatusServiceUnavailable {
		t.Fatalf("password login: status %d, want 503", status)
	}
	status, cfg := doReq(t, http.MethodGet, fx.url+"/api/admin/auth/config", "", nil)
	if status != http.StatusOK {
		t.Fatalf("auth config: status %d", status)
	}
	if cfg["has_users"] != false || cfg["token"] != false || cfg["password"] != false {
		t.Fatalf("auth config = %v, want everything off", cfg)
	}
}

// --- GitHub exchange against a mock provider ---

func TestGitHubExchangeAllowlist(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login/oauth/access_token":
			_ = r.ParseForm()
			if r.Form.Get("code") != "good-code" {
				_, _ = io.WriteString(w, `{"error":"bad_verification_code"}`)
				return
			}
			_, _ = io.WriteString(w, `{"access_token":"gho_test"}`)
		case "/user":
			if r.Header.Get("Authorization") != "Bearer gho_test" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(w, `{"login":"octocat","id":42}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(gh.Close)

	fx := newAuthFixture(t, "", map[string]string{
		settings.KeyGitHubClientID:     "gh-client",
		settings.KeyGitHubClientSecret: "gh-secret",
		settings.KeyGitHubAllowedUsers: "someoneelse",
	})
	fx.srv.githubTokenURL = gh.URL + "/login/oauth/access_token"
	fx.srv.githubUserURL = gh.URL + "/user"

	exchange := func() (int, map[string]any) {
		return doReq(t, http.MethodPost, fx.url+"/api/admin/auth/github/exchange", "",
			map[string]string{"code": "good-code", "redirect_uri": "http://site/api/auth/github/callback"})
	}
	// OAuth succeeds at GitHub, but octocat is not on the allowlist:
	// no account, no session.
	if status, body := exchange(); status != http.StatusForbidden {
		t.Fatalf("not-allowlisted exchange: status %d (%v), want 403", status, body)
	}
	if n, _ := fx.st.CountAdminUsers(context.Background()); n != 0 {
		t.Fatalf("users after rejected exchange = %d, want 0", n)
	}
	// Put octocat on the list (settings DB override, like the
	// settings page would) and the same flow mints a session.
	allow := "octocat"
	if _, _, err := fx.mgr.Update(map[string]*string{settings.KeyGitHubAllowedUsers: &allow}); err != nil {
		t.Fatalf("settings update: %v", err)
	}
	status, body := exchange()
	if status != http.StatusOK {
		t.Fatalf("allowlisted exchange: status %d (%v)", status, body)
	}
	session := sessionOf(t, body)
	if status, me := doReq(t, http.MethodGet, fx.url+"/api/admin/me", session, nil); status != http.StatusOK || me["method"] != "github" {
		t.Fatalf("me after github login = %d %v", status, me)
	}
	u, err := fx.st.AdminUserByExternal(context.Background(), store.SourceGitHub, "42")
	if err != nil || u == nil || u.Username != "octocat" || u.Role != store.RoleAdmin {
		t.Fatalf("linked user = %+v, %v", u, err)
	}
	// A second exchange reuses the account instead of duplicating it.
	if status, _ := exchange(); status != http.StatusOK {
		t.Fatalf("second exchange: status %d", status)
	}
	if n, _ := fx.st.CountAdminUsers(context.Background()); n != 1 {
		t.Fatalf("users after second exchange = %d, want 1", n)
	}
}

// --- OIDC exchange against a fake issuer ---

type fakeOIDC struct {
	srv     *httptest.Server
	key     *rsa.PrivateKey
	kid     string
	idToken string // returned by the token endpoint; set per case
}

func newFakeOIDC(t *testing.T) *fakeOIDC {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeOIDC{key: key, kid: "test-key-1"}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		iss := "http://" + r.Host
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer":                 iss,
			"authorization_endpoint": iss + "/auth",
			"token_endpoint":         iss + "/token",
			"jwks_uri":               iss + "/jwks",
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		n := base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes())
		e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes())
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]string{{"kid": f.kid, "kty": "RSA", "n": n, "e": e}},
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"id_token": f.idToken, "access_token": "x"})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// sign builds an RS256 id_token for the fake issuer. When signKey is
// nil the provider's own key is used.
func (f *fakeOIDC) sign(t *testing.T, claims map[string]any, signKey *rsa.PrivateKey) string {
	t.Helper()
	if signKey == nil {
		signKey = f.key
	}
	hdr, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": f.kid, "typ": "JWT"})
	payload, _ := json.Marshal(claims)
	input := base64.RawURLEncoding.EncodeToString(hdr) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, signKey, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func (f *fakeOIDC) claims(overrides map[string]any) map[string]any {
	c := map[string]any{
		"iss":            f.srv.URL,
		"sub":            "user-123",
		"aud":            "oidc-client",
		"exp":            time.Now().Add(10 * time.Minute).Unix(),
		"nonce":          "nonce-1",
		"email":          "Admin@Example.com",
		"email_verified": true,
	}
	for k, v := range overrides {
		c[k] = v
	}
	return c
}

func oidcFixture(t *testing.T, f *fakeOIDC, extra map[string]string) *authFixture {
	t.Helper()
	base := map[string]string{
		settings.KeyOIDCIssuer:       f.srv.URL,
		settings.KeyOIDCClientID:     "oidc-client",
		settings.KeyOIDCClientSecret: "oidc-secret",
	}
	for k, v := range extra {
		base[k] = v
	}
	return newAuthFixture(t, "", base)
}

func oidcExchange(t *testing.T, fx *authFixture) (int, map[string]any) {
	t.Helper()
	return doReq(t, http.MethodPost, fx.url+"/api/admin/auth/oidc/exchange", "",
		map[string]string{"code": "any-code", "redirect_uri": "http://site/api/auth/oidc/callback", "nonce": "nonce-1"})
}

func TestOIDCExchangeFakeIssuer(t *testing.T) {
	f := newFakeOIDC(t)
	fx := oidcFixture(t, f, map[string]string{
		settings.KeyOIDCAllowedEmails: "admin@example.com",
	})

	// Happy path: allowed email, account auto-created, session works.
	f.idToken = f.sign(t, f.claims(nil), nil)
	status, body := oidcExchange(t, fx)
	if status != http.StatusOK {
		t.Fatalf("happy exchange: status %d (%v)", status, body)
	}
	session := sessionOf(t, body)
	if status, me := doReq(t, http.MethodGet, fx.url+"/api/admin/me", session, nil); status != http.StatusOK ||
		me["method"] != "oidc" || me["username"] != "admin@example.com" {
		t.Fatalf("me after oidc login = %d %v", status, me)
	}
	if n, _ := fx.st.CountAdminUsers(context.Background()); n != 1 {
		t.Fatalf("users = %d, want 1", n)
	}

	// Email not on the allowlist: rejected, no new account.
	f.idToken = f.sign(t, f.claims(map[string]any{"sub": "user-999", "email": "other@example.com"}), nil)
	if status, _ := oidcExchange(t, fx); status != http.StatusForbidden {
		t.Fatalf("unlisted email: status %d, want 403", status)
	}
	// Wrong nonce.
	f.idToken = f.sign(t, f.claims(map[string]any{"nonce": "nonce-2"}), nil)
	if status, _ := oidcExchange(t, fx); status != http.StatusUnauthorized {
		t.Fatalf("wrong nonce: status %d, want 401", status)
	}
	// Expired token.
	f.idToken = f.sign(t, f.claims(map[string]any{"exp": time.Now().Add(-time.Minute).Unix()}), nil)
	if status, _ := oidcExchange(t, fx); status != http.StatusUnauthorized {
		t.Fatalf("expired token: status %d, want 401", status)
	}
	// Signed by a different key.
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f.idToken = f.sign(t, f.claims(nil), other)
	if status, _ := oidcExchange(t, fx); status != http.StatusUnauthorized {
		t.Fatalf("bad signature: status %d, want 401", status)
	}
	if n, _ := fx.st.CountAdminUsers(context.Background()); n != 1 {
		t.Fatalf("users after rejections = %d, want still 1", n)
	}

	// Unverified email (fresh fixture: the exchange endpoint's login
	// limiter is per client and the cases above have spent its burst).
	fx2 := oidcFixture(t, f, map[string]string{
		settings.KeyOIDCAllowedEmails: "admin@example.com",
	})
	f.idToken = f.sign(t, f.claims(map[string]any{"email_verified": false}), nil)
	if status, _ := oidcExchange(t, fx2); status != http.StatusForbidden {
		t.Fatalf("unverified email: status %d, want 403", status)
	}
}

func TestOIDCExchangeDomainAllowlist(t *testing.T) {
	f := newFakeOIDC(t)
	fx := oidcFixture(t, f, map[string]string{
		settings.KeyOIDCAllowedDomain: "example.com",
	})
	f.idToken = f.sign(t, f.claims(map[string]any{"email": "someone@example.com"}), nil)
	if status, body := oidcExchange(t, fx); status != http.StatusOK {
		t.Fatalf("domain-allowed exchange: status %d (%v)", status, body)
	}
	f.idToken = f.sign(t, f.claims(map[string]any{"sub": "user-555", "email": "someone@other.org"}), nil)
	if status, _ := oidcExchange(t, fx); status != http.StatusForbidden {
		t.Fatalf("other-domain exchange: status %d, want 403", status)
	}

	// Fail closed: neither emails nor domain configured.
	fx2 := oidcFixture(t, f, nil)
	f.idToken = f.sign(t, f.claims(nil), nil)
	if status, _ := oidcExchange(t, fx2); status != http.StatusForbidden {
		t.Fatalf("no-allowlist exchange: status %d, want 403 (fail closed)", status)
	}
}
