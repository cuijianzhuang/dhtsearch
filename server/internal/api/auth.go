package api

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"dhtsearch/server/internal/settings"
	"dhtsearch/server/internal/store"
)

// Admin authentication: who may call /api/admin/* and how they proved
// it. Three credential kinds resolve to one principal:
//
//   - the ADMIN_TOKEN recovery credential (kind "token"): configured
//     from the environment only, always role admin, kept forever as
//     the fallback that cannot lock the operator out of the account
//     system itself.
//   - a local account's username + password (kind "user").
//   - a federated identity — GitHub OAuth or OIDC SSO — linked to an
//     account row on first login (kind "user").
//
// Every successful login mints a server-side session (admin_sessions
// holds only the token's SHA-256); the web app stores the session
// token in its httpOnly cookie and presents it as the Bearer
// credential, so logout and password changes can actually revoke
// access — the raw ADMIN_TOKEN is never what the cookie holds after
// a token login either.
//
// Roles: "admin" reaches everything; "reviewer" reaches only the
// moderation surfaces (overview, torrent review/block/delete, the
// blocked list). Settings and account management are admin-only.

const (
	sessionTTL = 24 * time.Hour
	bcryptCost = 12

	roleAdmin    = store.RoleAdmin
	roleReviewer = store.RoleReviewer
)

// dummyPasswordHash is a real bcrypt hash of a throwaway string.
// Password logins for unknown (or passwordless) usernames compare
// against it so a failed lookup costs the same time as a failed
// compare and the endpoint does not oracle which usernames exist.
const dummyPasswordHash = "$2a$12$XvDQPki9bPhzi8YHukqPquh27tOaIRu2rsGiHIIuvGxzAh5kyj9Si"

// principal is the authenticated identity behind one admin request.
type principal struct {
	Kind     string `json:"kind"` // "token" | "user"
	UserID   string `json:"-"`    // empty for the token principal
	Username string `json:"username"`
	Role     string `json:"role"`   // "admin" | "reviewer"
	Method   string `json:"method"` // "token" | "password" | "github" | "oidc"
	// HasPassword reports whether a user principal can also log in
	// with a password (local accounts). The UI uses it to decide
	// whether to offer a self-service password change.
	HasPassword bool `json:"has_password,omitempty"`
}

// adminEnabled reports whether the admin surface answers at all: a
// configured recovery token, at least one account, or a configured
// federated login. A deployment with none of those exposes nothing
// (every admin route answers 503), exactly as before accounts
// existed.
func (s *Server) adminEnabled() bool {
	if s.opts.AdminToken != "" {
		return true
	}
	if s.githubConfigured() || s.oidcConfigured() {
		return true
	}
	n, err := s.cachedUserCount()
	return err == nil && n > 0
}

// cachedUserCount memoizes CountAdminUsers for the enabled check so
// the no-token path does not add a COUNT to every admin request.
func (s *Server) cachedUserCount() (int, error) {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	if time.Since(s.userCountAt) < 10*time.Second {
		return s.userCount, s.userCountErr
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	n, err := s.st.CountAdminUsers(ctx)
	s.userCount, s.userCountAt, s.userCountErr = n, time.Now(), err
	return n, err
}

func (s *Server) settingGet(key string) string {
	if s.opts.Settings == nil {
		return ""
	}
	return s.opts.Settings.Get(key)
}

func (s *Server) settingList(key string) []string {
	if s.opts.Settings == nil {
		return nil
	}
	return s.opts.Settings.List(key)
}

func (s *Server) githubConfigured() bool {
	return s.settingGet(settings.KeyGitHubClientID) != "" &&
		s.settingGet(settings.KeyGitHubClientSecret) != ""
}

func (s *Server) oidcConfigured() bool {
	return s.settingGet(settings.KeyOIDCIssuer) != "" &&
		s.settingGet(settings.KeyOIDCClientID) != "" &&
		s.settingGet(settings.KeyOIDCClientSecret) != ""
}

// sessionHash is what the sessions table stores for a session token.
func sessionHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// mintSession creates a server-side session and returns the raw
// token (the only moment it exists outside the caller's hands).
func (s *Server) mintSession(ctx context.Context, userID, method string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	now := time.Now()
	err := s.st.CreateAdminSession(ctx, store.AdminSession{
		TokenHash: sessionHash(token),
		UserID:    userID,
		Method:    method,
		CreatedAt: now.Unix(),
		ExpiresAt: now.Add(sessionTTL).Unix(),
	})
	if err != nil {
		return "", err
	}
	return token, nil
}

// authenticate resolves the request's Bearer credential to a
// principal. On failure it has already written the response (503
// when the admin surface is off, 401 otherwise) and returns false.
func (s *Server) authenticate(w http.ResponseWriter, r *http.Request) (*principal, bool) {
	if !s.adminEnabled() {
		writeError(w, http.StatusServiceUnavailable, "admin disabled")
		return nil, false
	}
	bearer := ""
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		bearer = strings.TrimPrefix(auth, "Bearer ")
	}
	if bearer == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return nil, false
	}
	// The recovery token itself, presented directly (API scripts,
	// pre-session cookies). Constant-time, as before.
	if s.opts.AdminToken != "" &&
		subtle.ConstantTimeCompare([]byte(bearer), []byte(s.opts.AdminToken)) == 1 {
		return &principal{Kind: "token", Username: "ADMIN_TOKEN", Role: roleAdmin, Method: "token"}, true
	}
	// Otherwise the credential must be a live session.
	ctx, cancel := s.adminCtx(r)
	defer cancel()
	sess, err := s.st.AdminSessionByHash(ctx, sessionHash(bearer))
	if err != nil {
		s.logger.Printf("api: session lookup: %v", err)
		writeError(w, http.StatusInternalServerError, "auth failed")
		return nil, false
	}
	if sess == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return nil, false
	}
	if time.Now().Unix() > sess.ExpiresAt {
		_ = s.st.DeleteAdminSession(ctx, sess.TokenHash)
		writeError(w, http.StatusUnauthorized, "session expired")
		return nil, false
	}
	if sess.UserID == "" {
		// A session minted by the recovery-token login.
		return &principal{Kind: "token", Username: "ADMIN_TOKEN", Role: roleAdmin, Method: sess.Method}, true
	}
	u, err := s.st.AdminUserByID(ctx, sess.UserID)
	if err != nil {
		s.logger.Printf("api: session user lookup: %v", err)
		writeError(w, http.StatusInternalServerError, "auth failed")
		return nil, false
	}
	if u == nil || u.Disabled {
		// The account vanished or was disabled after login: the
		// session dies with it.
		_ = s.st.DeleteAdminSession(ctx, sess.TokenHash)
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return nil, false
	}
	return &principal{
		Kind: "user", UserID: u.ID, Username: u.Username, Role: u.Role,
		Method: sess.Method, HasPassword: u.PasswordHash != "",
	}, true
}

// requireAdmin gates a handler for any authenticated principal.
func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	_, ok := s.authenticate(w, r)
	return ok
}

// requireRole gates a handler for one role ("admin" satisfies every
// role check) and returns the principal for handlers that need it.
func (s *Server) requireRole(w http.ResponseWriter, r *http.Request, role string) (*principal, bool) {
	p, ok := s.authenticate(w, r)
	if !ok {
		return nil, false
	}
	if role == roleAdmin && p.Role != roleAdmin {
		writeError(w, http.StatusForbidden, "admin role required")
		return nil, false
	}
	return p, true
}

// --- login endpoints ---

// loginBody is the shared response of every login path: the session
// token the web app stores in its cookie, plus who it belongs to.
type loginResult struct {
	OK        bool       `json:"ok"`
	Session   string     `json:"session,omitempty"`
	Principal *principal `json:"principal,omitempty"`
}

// finishLogin records the login and mints the session for a user
// account, then writes the shared login response.
func (s *Server) finishLogin(w http.ResponseWriter, r *http.Request, u *store.AdminUser, method string) {
	ctx, cancel := s.adminCtx(r)
	defer cancel()
	_ = s.st.TouchAdminUserLogin(ctx, u.ID, time.Now().Unix())
	token, err := s.mintSession(ctx, u.ID, method)
	if err != nil {
		s.logger.Printf("api: mint session: %v", err)
		writeError(w, http.StatusInternalServerError, "login failed")
		return
	}
	writeJSON(w, http.StatusOK, loginResult{
		OK:      true,
		Session: token,
		Principal: &principal{
			Kind: "user", UserID: u.ID, Username: u.Username, Role: u.Role,
			Method: method, HasPassword: u.PasswordHash != "",
		},
	})
}

// handleAdminPasswordLogin verifies a username + password pair.
// Throttled per client AND per username, so a distributed guess
// against one account still slows down.
func (s *Server) handleAdminPasswordLogin(w http.ResponseWriter, r *http.Request) {
	if !s.adminEnabled() {
		writeError(w, http.StatusServiceUnavailable, "admin disabled")
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	if err := dec.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad request")
		return
	}
	key := clientKey(r) + "|pw|" + strings.ToLower(strings.TrimSpace(body.Username))
	if !s.loginLimiter.allow(key) {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "too many login attempts")
		return
	}
	ctx, cancel := s.adminCtx(r)
	defer cancel()
	u, err := s.st.AdminUserByUsername(ctx, strings.TrimSpace(body.Username))
	if err != nil {
		s.logger.Printf("api: password login lookup: %v", err)
		writeError(w, http.StatusInternalServerError, "login failed")
		return
	}
	hash := dummyPasswordHash
	if u != nil && u.PasswordHash != "" {
		hash = u.PasswordHash
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(body.Password)) != nil ||
		u == nil || u.PasswordHash == "" || u.Disabled {
		writeError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	s.finishLogin(w, r, u, "password")
}

// handleAdminLogout ends the presented session, if it is one. The
// raw recovery token cannot be "logged out" server-side (it is a
// static credential); the web layer just drops its cookie.
func (s *Server) handleAdminLogout(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authenticate(w, r); !ok {
		return
	}
	bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if s.opts.AdminToken == "" ||
		subtle.ConstantTimeCompare([]byte(bearer), []byte(s.opts.AdminToken)) != 1 {
		ctx, cancel := s.adminCtx(r)
		defer cancel()
		_ = s.st.DeleteAdminSession(ctx, sessionHash(bearer))
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleAdminMe reports the caller's principal, for the console UI
// (role-gated navigation, account page) to shape itself with.
func (s *Server) handleAdminMe(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	n, _ := s.cachedUserCount()
	writeJSON(w, http.StatusOK, map[string]any{
		"kind":         p.Kind,
		"username":     p.Username,
		"role":         p.Role,
		"method":       p.Method,
		"has_password": p.HasPassword,
		"has_users":    n > 0,
	})
}

// handleAuthConfig is public on purpose: the login page must know
// which login methods to render before anyone is authenticated. It
// carries no secrets — client IDs are public by design, and the
// allowlists stay server-side.
func (s *Server) handleAuthConfig(w http.ResponseWriter, r *http.Request) {
	n, _ := s.cachedUserCount()
	oidcName := s.settingGet(settings.KeyOIDCProviderName)
	if oidcName == "" {
		oidcName = "SSO"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"has_users": n > 0,
		"token":     s.opts.AdminToken != "",
		"password":  n > 0,
		"github": map[string]any{
			"enabled":   s.githubConfigured(),
			"client_id": s.settingGet(settings.KeyGitHubClientID),
		},
		"oidc": map[string]any{
			"enabled":   s.oidcConfigured(),
			"issuer":    s.settingGet(settings.KeyOIDCIssuer),
			"client_id": s.settingGet(settings.KeyOIDCClientID),
			"name":      oidcName,
		},
	})
}

// --- account management (admin role) ---

type adminUserJSON struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	Role        string `json:"role"`
	Source      string `json:"source"`
	Disabled    bool   `json:"disabled"`
	CreatedAt   int64  `json:"created_at"`
	LastLoginAt int64  `json:"last_login_at"`
}

func toAdminUserJSON(u store.AdminUser) adminUserJSON {
	return adminUserJSON{
		ID: u.ID, Username: u.Username, Role: u.Role, Source: u.Source,
		Disabled: u.Disabled, CreatedAt: u.CreatedAt, LastLoginAt: u.LastLoginAt,
	}
}

// validUsername bounds local account names. Federated usernames come
// from the provider (a GitHub login or an email) and are checked
// with the same rules at link time.
func validUsername(name string) bool {
	if len(name) < 3 || len(name) > 64 {
		return false
	}
	for _, c := range name {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.' || c == '_' || c == '-' || c == '@' || c == '+':
		default:
			return false
		}
	}
	return true
}

func validPassword(pw string) bool {
	return len(pw) >= 8 && len(pw) <= 128
}

func hashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcryptCost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (s *Server) handleAdminUsersList(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireRole(w, r, roleAdmin); !ok {
		return
	}
	ctx, cancel := s.adminCtx(r)
	defer cancel()
	users, err := s.st.ListAdminUsers(ctx)
	if err != nil {
		s.logger.Printf("api: list users: %v", err)
		writeError(w, http.StatusInternalServerError, "list failed")
		return
	}
	out := make([]adminUserJSON, 0, len(users))
	for _, u := range users {
		out = append(out, toAdminUserJSON(u))
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": out, "total": len(out)})
}

func (s *Server) handleAdminUsersCreate(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireRole(w, r, roleAdmin); !ok {
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad request")
		return
	}
	body.Username = strings.TrimSpace(body.Username)
	if !validUsername(body.Username) {
		writeError(w, http.StatusBadRequest, "username must be 3-64 chars of letters, digits, . _ - @ +")
		return
	}
	if !validPassword(body.Password) {
		writeError(w, http.StatusBadRequest, "password must be 8-128 characters")
		return
	}
	if body.Role == "" {
		body.Role = roleReviewer
	}
	if body.Role != roleAdmin && body.Role != roleReviewer {
		writeError(w, http.StatusBadRequest, "role must be admin or reviewer")
		return
	}
	hash, err := hashPassword(body.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "create failed")
		return
	}
	ctx, cancel := s.adminCtx(r)
	defer cancel()
	u := &store.AdminUser{
		Username: body.Username, PasswordHash: hash, Role: body.Role, Source: store.SourceLocal,
	}
	if err := s.st.CreateAdminUser(ctx, u, time.Now().Unix()); err != nil {
		if store.IsUniqueViolation(err) {
			writeError(w, http.StatusConflict, "username already exists")
			return
		}
		s.logger.Printf("api: create user: %v", err)
		writeError(w, http.StatusInternalServerError, "create failed")
		return
	}
	// A new account changes the bootstrap state; drop the memo so
	// the login page and the enabled check see it at once.
	s.authMu.Lock()
	s.userCountAt = time.Time{}
	s.authMu.Unlock()
	writeJSON(w, http.StatusOK, toAdminUserJSON(*u))
}

// isLastEnabledAdmin reports whether u is the one remaining account
// that could administer the console. Disabling, demoting or deleting
// it would strand the account system (the recovery token still gets
// in, but no account could).
func (s *Server) isLastEnabledAdmin(ctx context.Context, u *store.AdminUser) bool {
	if u.Role != roleAdmin || u.Disabled {
		return false
	}
	n, err := s.st.CountEnabledAdminUsers(ctx)
	return err == nil && n <= 1
}

func (s *Server) handleAdminUserUpdate(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireRole(w, r, roleAdmin); !ok {
		return
	}
	var body struct {
		Role     *string `json:"role"`
		Disabled *bool   `json:"disabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad request")
		return
	}
	ctx, cancel := s.adminCtx(r)
	defer cancel()
	u, err := s.st.AdminUserByID(ctx, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "update failed")
		return
	}
	if u == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if body.Role != nil {
		if *body.Role != roleAdmin && *body.Role != roleReviewer {
			writeError(w, http.StatusBadRequest, "role must be admin or reviewer")
			return
		}
		if *body.Role != roleAdmin && s.isLastEnabledAdmin(ctx, u) {
			writeError(w, http.StatusBadRequest, "cannot demote the last enabled admin")
			return
		}
		u.Role = *body.Role
	}
	if body.Disabled != nil {
		if *body.Disabled && s.isLastEnabledAdmin(ctx, u) {
			writeError(w, http.StatusBadRequest, "cannot disable the last enabled admin")
			return
		}
		u.Disabled = *body.Disabled
	}
	if err := s.st.UpdateAdminUser(ctx, u, time.Now().Unix()); err != nil {
		s.logger.Printf("api: update user: %v", err)
		writeError(w, http.StatusInternalServerError, "update failed")
		return
	}
	if u.Disabled {
		_ = s.st.DeleteAdminUserSessions(ctx, u.ID)
	}
	writeJSON(w, http.StatusOK, toAdminUserJSON(*u))
}

func (s *Server) handleAdminUserResetPassword(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireRole(w, r, roleAdmin); !ok {
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad request")
		return
	}
	if !validPassword(body.Password) {
		writeError(w, http.StatusBadRequest, "password must be 8-128 characters")
		return
	}
	ctx, cancel := s.adminCtx(r)
	defer cancel()
	u, err := s.st.AdminUserByID(ctx, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "reset failed")
		return
	}
	if u == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if u.Source != store.SourceLocal {
		writeError(w, http.StatusBadRequest, "federated accounts have no local password")
		return
	}
	hash, err := hashPassword(body.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "reset failed")
		return
	}
	u.PasswordHash = hash
	if err := s.st.UpdateAdminUser(ctx, u, time.Now().Unix()); err != nil {
		s.logger.Printf("api: reset password: %v", err)
		writeError(w, http.StatusInternalServerError, "reset failed")
		return
	}
	// A reset password retires every existing session of the account.
	_ = s.st.DeleteAdminUserSessions(ctx, u.ID)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleAdminUserDelete(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireRole(w, r, roleAdmin); !ok {
		return
	}
	ctx, cancel := s.adminCtx(r)
	defer cancel()
	u, err := s.st.AdminUserByID(ctx, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "delete failed")
		return
	}
	if u == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if s.isLastEnabledAdmin(ctx, u) {
		writeError(w, http.StatusBadRequest, "cannot delete the last enabled admin")
		return
	}
	_ = s.st.DeleteAdminUserSessions(ctx, u.ID)
	deleted, err := s.st.DeleteAdminUser(ctx, u.ID)
	if err != nil || !deleted {
		writeError(w, http.StatusInternalServerError, "delete failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleAdminChangeMyPassword is the self-service password change
// for local accounts. It verifies the current password, then kills
// every OTHER session of the account: a password change is exactly
// the moment other devices should fall off.
func (s *Server) handleAdminChangeMyPassword(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	if p.Kind != "user" {
		writeError(w, http.StatusBadRequest, "the recovery token has no password to change")
		return
	}
	var body struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad request")
		return
	}
	if !validPassword(body.NewPassword) {
		writeError(w, http.StatusBadRequest, "password must be 8-128 characters")
		return
	}
	ctx, cancel := s.adminCtx(r)
	defer cancel()
	u, err := s.st.AdminUserByID(ctx, p.UserID)
	if err != nil || u == nil {
		writeError(w, http.StatusInternalServerError, "change failed")
		return
	}
	if u.PasswordHash == "" ||
		bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(body.CurrentPassword)) != nil {
		writeError(w, http.StatusUnauthorized, "current password is wrong")
		return
	}
	hash, err := hashPassword(body.NewPassword)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "change failed")
		return
	}
	u.PasswordHash = hash
	if err := s.st.UpdateAdminUser(ctx, u, time.Now().Unix()); err != nil {
		s.logger.Printf("api: change password: %v", err)
		writeError(w, http.StatusInternalServerError, "change failed")
		return
	}
	bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	_ = s.st.DeleteAdminUserSessionsExcept(ctx, u.ID, sessionHash(bearer))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// --- GitHub OAuth ---

// The two GitHub endpoints are fields so tests can point them at a
// mock provider; production uses the real ones.
func (s *Server) githubTokenEndpoint() string {
	if s.githubTokenURL != "" {
		return s.githubTokenURL
	}
	return "https://github.com/login/oauth/access_token"
}

func (s *Server) githubUserEndpoint() string {
	if s.githubUserURL != "" {
		return s.githubUserURL
	}
	return "https://api.github.com/user"
}

// handleGitHubExchange completes the OAuth authorization-code flow
// the web app started: it swaps the code for an access token (the
// client secret never leaves this process), reads the GitHub
// profile, checks the allowlist and links or creates the account.
func (s *Server) handleGitHubExchange(w http.ResponseWriter, r *http.Request) {
	if !s.githubConfigured() {
		writeError(w, http.StatusServiceUnavailable, "github login not configured")
		return
	}
	if !s.loginLimiter.allow(clientKey(r) + "|github") {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "too many login attempts")
		return
	}
	var body struct {
		Code        string `json:"code"`
		RedirectURI string `json:"redirect_uri"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil ||
		body.Code == "" || body.RedirectURI == "" {
		writeError(w, http.StatusBadRequest, "bad request")
		return
	}
	ctx, cancel := s.adminCtx(r)
	defer cancel()

	form := url.Values{
		"client_id":     {s.settingGet(settings.KeyGitHubClientID)},
		"client_secret": {s.settingGet(settings.KeyGitHubClientSecret)},
		"code":          {body.Code},
		"redirect_uri":  {body.RedirectURI},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.githubTokenEndpoint(), strings.NewReader(form.Encode()))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "exchange failed")
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := s.authHTTP.Do(req)
	if err != nil {
		s.logger.Printf("api: github token exchange: %v", err)
		writeError(w, http.StatusBadGateway, "github unreachable")
		return
	}
	defer resp.Body.Close()
	var tok struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&tok); err != nil || tok.AccessToken == "" {
		s.logger.Printf("api: github token exchange: status %d error %q", resp.StatusCode, tok.Error)
		writeError(w, http.StatusUnauthorized, "github authorization failed")
		return
	}

	req, err = http.NewRequestWithContext(ctx, http.MethodGet, s.githubUserEndpoint(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "exchange failed")
		return
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "dhtsearch")
	resp, err = s.authHTTP.Do(req)
	if err != nil {
		s.logger.Printf("api: github user fetch: %v", err)
		writeError(w, http.StatusBadGateway, "github unreachable")
		return
	}
	defer resp.Body.Close()
	var gh struct {
		Login string `json:"login"`
		ID    int64  `json:"id"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&gh); err != nil || gh.Login == "" || gh.ID == 0 {
		writeError(w, http.StatusUnauthorized, "github authorization failed")
		return
	}

	allowed := false
	for _, name := range s.settingList(settings.KeyGitHubAllowedUsers) {
		if strings.EqualFold(name, gh.Login) {
			allowed = true
			break
		}
	}
	if !allowed {
		// OAuth succeeded but this GitHub user is not on the list:
		// no account is created and no session is minted.
		writeError(w, http.StatusForbidden, "github user not on the allowlist")
		return
	}

	extID := strconv.FormatInt(gh.ID, 10)
	u, err := s.st.AdminUserByExternal(ctx, store.SourceGitHub, extID)
	if err != nil {
		s.logger.Printf("api: github user lookup: %v", err)
		writeError(w, http.StatusInternalServerError, "login failed")
		return
	}
	if u == nil {
		username := gh.Login
		if taken, _ := s.st.AdminUserByUsername(ctx, username); taken != nil {
			// A local account already owns this name; keep the two
			// namespaces apart instead of silently merging them.
			username = gh.Login + "@github"
		}
		u = &store.AdminUser{
			Username: username, Role: roleAdmin, Source: store.SourceGitHub, ExternalID: extID,
		}
		if err := s.st.CreateAdminUser(ctx, u, time.Now().Unix()); err != nil {
			s.logger.Printf("api: github user create: %v", err)
			writeError(w, http.StatusInternalServerError, "login failed")
			return
		}
	}
	if u.Disabled {
		writeError(w, http.StatusForbidden, "account disabled")
		return
	}
	s.finishLogin(w, r, u, "github")
}

// --- OIDC SSO ---

// oidcProvider is a discovered OIDC provider: the endpoints this
// server needs plus its signing keys, cached between logins.
type oidcProvider struct {
	issuer   string
	tokenURL string
	jwksURL  string
	keys     map[string]*rsa.PublicKey
	keysAt   time.Time
	discAt   time.Time
}

// oidcDiscovery fetches (and caches) the provider's discovery
// document. The issuer in the document must match the configured
// issuer — a mismatch means the operator pasted the wrong URL or
// the provider is misbehaving, and tokens from it must not be
// trusted either way.
func (s *Server) oidcDiscovery(ctx context.Context, issuer string) (*oidcProvider, error) {
	issuer = strings.TrimSuffix(issuer, "/")
	s.oidcMu.Lock()
	if p, ok := s.oidcProviders[issuer]; ok && time.Since(p.discAt) < time.Hour {
		s.oidcMu.Unlock()
		return p, nil
	}
	s.oidcMu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, issuer+"/.well-known/openid-configuration", nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.authHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("discovery: status %d", resp.StatusCode)
	}
	var doc struct {
		Issuer   string `json:"issuer"`
		TokenURL string `json:"token_endpoint"`
		JWKSURL  string `json:"jwks_uri"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 256<<10)).Decode(&doc); err != nil {
		return nil, err
	}
	if strings.TrimSuffix(doc.Issuer, "/") != issuer || doc.TokenURL == "" || doc.JWKSURL == "" {
		return nil, fmt.Errorf("discovery: issuer mismatch or missing endpoints")
	}
	p := &oidcProvider{issuer: doc.Issuer, tokenURL: doc.TokenURL, jwksURL: doc.JWKSURL, discAt: time.Now()}
	s.oidcMu.Lock()
	if s.oidcProviders == nil {
		s.oidcProviders = map[string]*oidcProvider{}
	}
	s.oidcProviders[issuer] = p
	s.oidcMu.Unlock()
	return p, nil
}

// oidcKey returns the provider's RSA key for kid, refetching the
// JWKS once when the kid is unknown (key rotation).
func (s *Server) oidcKey(ctx context.Context, p *oidcProvider, kid string, force bool) (*rsa.PublicKey, error) {
	if !force {
		s.oidcMu.Lock()
		if p.keys != nil && time.Since(p.keysAt) < time.Hour {
			k, ok := p.keys[kid]
			s.oidcMu.Unlock()
			if ok {
				return k, nil
			}
			// Unknown kid with a fresh JWKS: fall through to one
			// refetch in case the provider just rotated keys.
		} else {
			s.oidcMu.Unlock()
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.jwksURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.authHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jwks: status %d", resp.StatusCode)
	}
	var jwks struct {
		Keys []struct {
			Kid string `json:"kid"`
			Kty string `json:"kty"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 256<<10)).Decode(&jwks); err != nil {
		return nil, err
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range jwks.Keys {
		if k.Kty != "RSA" {
			continue
		}
		nb, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil {
			continue
		}
		eb, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil || len(eb) == 0 || len(eb) > 8 {
			continue
		}
		e := 0
		for _, b := range eb {
			e = e<<8 | int(b)
		}
		keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(nb), E: e}
	}
	s.oidcMu.Lock()
	p.keys, p.keysAt = keys, time.Now()
	s.oidcMu.Unlock()
	if k, ok := keys[kid]; ok {
		return k, nil
	}
	return nil, fmt.Errorf("jwks: no key for kid %q", kid)
}

// audList accepts an OIDC aud claim spelled as a string or an array.
type audList []string

func (a *audList) UnmarshalJSON(b []byte) error {
	var single string
	if err := json.Unmarshal(b, &single); err == nil {
		*a = []string{single}
		return nil
	}
	var multi []string
	if err := json.Unmarshal(b, &multi); err != nil {
		return err
	}
	*a = multi
	return nil
}

func (a audList) contains(want string) bool {
	for _, v := range a {
		if v == want {
			return true
		}
	}
	return false
}

// optBool is an optional bool claim: it records whether the claim
// was present at all, and accepts it spelled as a JSON bool or as
// the string "true"/"false" (both occur in the wild — Google sends
// a bool, some providers send a string).
type optBool struct {
	Set   bool
	Value bool
}

func (o *optBool) UnmarshalJSON(b []byte) error {
	var v bool
	if err := json.Unmarshal(b, &v); err == nil {
		o.Set, o.Value = true, v
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	o.Set, o.Value = true, s == "true"
	return nil
}

type idTokenClaims struct {
	Issuer        string  `json:"iss"`
	Subject       string  `json:"sub"`
	Audience      audList `json:"aud"`
	ExpiresAt     int64   `json:"exp"`
	Nonce         string  `json:"nonce"`
	Email         string  `json:"email"`
	EmailVerified optBool `json:"email_verified"`
}

// verifyIDToken checks an OIDC id_token end to end: RS256 signature
// against the provider's JWKS, then issuer, audience, expiry and
// the nonce the login flow started with.
func (s *Server) verifyIDToken(ctx context.Context, p *oidcProvider, raw, clientID, nonce string) (*idTokenClaims, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("malformed jwt")
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("malformed jwt header")
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return nil, fmt.Errorf("malformed jwt header")
	}
	if header.Alg != "RS256" {
		return nil, fmt.Errorf("unexpected alg %q", header.Alg)
	}
	key, err := s.oidcKey(ctx, p, header.Kid, false)
	if err != nil {
		key, err = s.oidcKey(ctx, p, header.Kid, true)
		if err != nil {
			return nil, err
		}
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("malformed signature")
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], sig); err != nil {
		return nil, fmt.Errorf("signature verification failed")
	}
	claimsBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("malformed claims")
	}
	var claims idTokenClaims
	if err := json.Unmarshal(claimsBytes, &claims); err != nil {
		return nil, fmt.Errorf("malformed claims")
	}
	if claims.Issuer != p.issuer {
		return nil, fmt.Errorf("issuer mismatch")
	}
	if !claims.Audience.contains(clientID) {
		return nil, fmt.Errorf("audience mismatch")
	}
	if time.Now().Unix() > claims.ExpiresAt {
		return nil, fmt.Errorf("token expired")
	}
	if claims.Nonce != nonce {
		return nil, fmt.Errorf("nonce mismatch")
	}
	if claims.Subject == "" {
		return nil, fmt.Errorf("missing sub")
	}
	return &claims, nil
}

// handleOIDCExchange completes the OIDC authorization-code flow:
// code swap at the discovered token endpoint, full id_token
// verification, then the email allowlist decides who gets in.
func (s *Server) handleOIDCExchange(w http.ResponseWriter, r *http.Request) {
	if !s.oidcConfigured() {
		writeError(w, http.StatusServiceUnavailable, "sso login not configured")
		return
	}
	if !s.loginLimiter.allow(clientKey(r) + "|oidc") {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "too many login attempts")
		return
	}
	var body struct {
		Code        string `json:"code"`
		RedirectURI string `json:"redirect_uri"`
		Nonce       string `json:"nonce"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil ||
		body.Code == "" || body.RedirectURI == "" || body.Nonce == "" {
		writeError(w, http.StatusBadRequest, "bad request")
		return
	}
	ctx, cancel := s.adminCtx(r)
	defer cancel()

	issuer := s.settingGet(settings.KeyOIDCIssuer)
	clientID := s.settingGet(settings.KeyOIDCClientID)
	provider, err := s.oidcDiscovery(ctx, issuer)
	if err != nil {
		s.logger.Printf("api: oidc discovery: %v", err)
		writeError(w, http.StatusBadGateway, "sso provider unreachable")
		return
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {body.Code},
		"redirect_uri":  {body.RedirectURI},
		"client_id":     {clientID},
		"client_secret": {s.settingGet(settings.KeyOIDCClientSecret)},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, provider.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "exchange failed")
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := s.authHTTP.Do(req)
	if err != nil {
		s.logger.Printf("api: oidc token exchange: %v", err)
		writeError(w, http.StatusBadGateway, "sso provider unreachable")
		return
	}
	defer resp.Body.Close()
	var tok struct {
		IDToken string `json:"id_token"`
		Error   string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 256<<10)).Decode(&tok); err != nil || tok.IDToken == "" {
		s.logger.Printf("api: oidc token exchange: status %d error %q", resp.StatusCode, tok.Error)
		writeError(w, http.StatusUnauthorized, "sso authorization failed")
		return
	}
	claims, err := s.verifyIDToken(ctx, provider, tok.IDToken, clientID, body.Nonce)
	if err != nil {
		s.logger.Printf("api: oidc id_token: %v", err)
		writeError(w, http.StatusUnauthorized, "sso authorization failed")
		return
	}
	email := strings.ToLower(strings.TrimSpace(claims.Email))
	if email == "" {
		writeError(w, http.StatusForbidden, "provider returned no email")
		return
	}
	if claims.EmailVerified.Set && !claims.EmailVerified.Value {
		// A provider that sends email_verified=false is telling us
		// the address is unproven; one that omits the claim entirely
		// is judged by the allowlist alone.
		writeError(w, http.StatusForbidden, "email not verified")
		return
	}
	allowed := false
	for _, e := range s.settingList(settings.KeyOIDCAllowedEmails) {
		if strings.EqualFold(e, email) {
			allowed = true
			break
		}
	}
	if !allowed {
		if domain := strings.ToLower(strings.TrimSpace(s.settingGet(settings.KeyOIDCAllowedDomain))); domain != "" {
			if i := strings.LastIndex(email, "@"); i >= 0 && email[i+1:] == domain {
				allowed = true
			}
		}
	}
	if !allowed {
		// Fail closed: with neither an email nor a domain on the
		// allowlist, a perfectly valid SSO identity still gets
		// nothing.
		writeError(w, http.StatusForbidden, "email not on the allowlist")
		return
	}

	u, err := s.st.AdminUserByExternal(ctx, store.SourceOIDC, claims.Subject)
	if err != nil {
		s.logger.Printf("api: oidc user lookup: %v", err)
		writeError(w, http.StatusInternalServerError, "login failed")
		return
	}
	if u == nil {
		u = &store.AdminUser{
			Username: email, Role: roleAdmin, Source: store.SourceOIDC, ExternalID: claims.Subject,
		}
		if err := s.st.CreateAdminUser(ctx, u, time.Now().Unix()); err != nil {
			if store.IsUniqueViolation(err) {
				writeError(w, http.StatusForbidden, "an account with this email already exists")
				return
			}
			s.logger.Printf("api: oidc user create: %v", err)
			writeError(w, http.StatusInternalServerError, "login failed")
			return
		}
	}
	if u.Disabled {
		writeError(w, http.StatusForbidden, "account disabled")
		return
	}
	s.finishLogin(w, r, u, "oidc")
}
