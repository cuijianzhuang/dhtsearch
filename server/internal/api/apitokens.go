package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"dhtsearch/server/internal/settings"
)

// Public API tokens: optional bearer credentials for the public
// endpoints (/api/search, /api/stats, /api/trending,
// /api/trending-terms), managed from the admin console. Two settings (see internal/settings) steer the
// gate, both read live per request:
//
//   - PUBLIC_API_ENABLED (default on): off answers the three public
//     endpoints with 503 for everyone. /api/healthz, /api/site and
//     the admin API are never gated.
//   - PUBLIC_API_REQUIRE_TOKEN (default off): on, a request must
//     carry a valid token — missing/invalid 401, disabled 403, over
//     the token's own per-minute quota 429. Off, the endpoints stay
//     anonymous exactly as before, but a request that does present a
//     valid token is counted against it, so operators can watch
//     adoption before flipping enforcement on.
//
// First-party exemption: the bundled web frontend calls these same
// endpoints server-side, anonymously from the gate's point of view —
// enforcing tokens naively would lock the site itself out (this
// actually happened in production). The frontend therefore presents
// the deployment's INTERNAL_API_TOKEN in the X-Internal-Token header;
// a match marks the request first-party: it skips the require-token
// gate and all token counting/quota, without being a token itself
// (no database row, no list entry, no log line). PUBLIC_API_ENABLED
// stays a master switch and answers 503 to first-party requests too:
// it is the documented "shut the public read API down" lever, and its
// settings description says the frontend stops with it.
//
// Hashing choice: tokens are 256-bit random secrets, so the store
// keeps a plain SHA-256 of the token — brute force is infeasible at
// that entropy, and a fast hash keeps the per-request lookup to one
// indexed query. bcrypt/scrypt exist for low-entropy human
// passwords; here they would only add CPU cost per request. The
// plaintext is returned exactly once, by the create endpoint.

const (
	apiTokenPrefix   = "dht_"
	apiTokenBytes    = 32 // 256-bit secret, hex-encoded after the prefix
	maxTokenRate     = 1_000_000
	defaultTokenRate = 600 // per minute, when no settings registry is wired
)

// generateAPIToken mints a new plaintext token: the "dht_" prefix
// plus 64 hex chars of crypto/rand.
func generateAPIToken() (string, error) {
	b := make([]byte, apiTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return apiTokenPrefix + hex.EncodeToString(b), nil
}

// apiTokenHash is the stored form of a plaintext token.
func apiTokenHash(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// apiTokenDisplayPrefix is the safe-to-show head of a token: the
// prefix plus the first 8 secret characters — enough to tell tokens
// apart in the console and in logs, useless for authentication.
func apiTokenDisplayPrefix(plaintext string) string {
	if len(plaintext) <= len(apiTokenPrefix)+8 {
		return plaintext
	}
	return plaintext[:len(apiTokenPrefix)+8]
}

// presentedAPIToken extracts the credential from a request:
// Authorization: Bearer first, then X-API-Key.
func presentedAPIToken(r *http.Request) string {
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		if tok := strings.TrimSpace(strings.TrimPrefix(auth, "Bearer ")); tok != "" {
			return tok
		}
	}
	return strings.TrimSpace(r.Header.Get("X-API-Key"))
}

// --- per-token rate limiter ---

// tokenLimiter is a token bucket per API token, refilled at the
// token's own rate_per_min. It mirrors ipLimiter's bookkeeping
// (idle sweep, hard cap) but the rate travels with each call because
// every token carries its own quota and the admin can change it at
// any time; a bucket whose stored rate no longer matches is reset.
type tokenLimiter struct {
	mu        sync.Mutex
	buckets   map[int64]*tokenBucket
	lastSweep time.Time
	now       func() time.Time
}

type tokenBucket struct {
	tokens     float64
	last       time.Time
	ratePerMin int
}

func newTokenLimiter() *tokenLimiter {
	return &tokenLimiter{buckets: make(map[int64]*tokenBucket), now: time.Now}
}

// allow consumes one unit of token id's quota. ratePerMin <= 0 means
// unlimited and never touches the map.
func (l *tokenLimiter) allow(id int64, ratePerMin int) bool {
	if ratePerMin <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if now.Sub(l.lastSweep) >= sweepEvery || len(l.buckets) > maxBuckets {
		l.lastSweep = now
		for k, b := range l.buckets {
			if now.Sub(b.last) > bucketIdleTTL {
				delete(l.buckets, k)
			}
		}
	}
	b, ok := l.buckets[id]
	if !ok || b.ratePerMin != ratePerMin {
		b = &tokenBucket{tokens: float64(ratePerMin), last: now, ratePerMin: ratePerMin}
		l.buckets[id] = b
	} else {
		b.tokens = min(float64(ratePerMin), b.tokens+now.Sub(b.last).Seconds()*float64(ratePerMin)/60)
		b.last = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// internalTokenHeader carries the first-party credential from the
// web frontend. A dedicated header (not Authorization) keeps it out
// of the user-token channel entirely: it is never looked up in the
// token store, so it cannot collide with, or be counted as, a
// managed API token.
const internalTokenHeader = "X-Internal-Token"

// isFirstParty reports whether the request carries the deployment's
// internal token. The comparison is constant-time; an unconfigured
// token (empty) never matches, including an empty presented header.
func (s *Server) isFirstParty(r *http.Request) bool {
	want := s.opts.InternalToken
	if want == "" {
		return false
	}
	got := r.Header.Get(internalTokenHeader)
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// --- the public gate ---

// publicPaths are the endpoints PUBLIC_API_* governs. /api/healthz
// (monitoring), /api/site (page rendering) and /api/admin/* (its own
// ADMIN_TOKEN auth) are deliberately outside it.
func isPublicAPIPath(path string) bool {
	switch path {
	case "/api/search", "/api/stats", "/api/trending", "/api/trending-terms":
		return true
	}
	return false
}

// publicGate enforces the PUBLIC_API_* settings for one public
// request. It returns false when it has already answered (503/401/
// 403/429); true means the request may proceed to the mux. The
// per-IP limiter in Handler runs before this and keeps applying —
// a token raises a caller's guarantees, it does not exempt them
// from the shared flood defenses.
func (s *Server) publicGate(w http.ResponseWriter, r *http.Request) bool {
	mgr := s.opts.Settings
	if mgr == nil {
		return true // settings-free wiring (tests): historical behaviour
	}
	if !mgr.Bool(settings.KeyPublicAPIEnabled) {
		writeError(w, http.StatusServiceUnavailable, "public api disabled")
		return false
	}
	if s.isFirstParty(r) {
		// The site's own frontend: exempt from the require-token gate
		// and from token counting/quota. Deliberately placed after the
		// enabled check — PUBLIC_API_ENABLED off means off for
		// everyone, first-party included.
		return true
	}
	require := mgr.Bool(settings.KeyPublicAPIRequireToken)
	presented := presentedAPIToken(r)
	if presented == "" {
		if require {
			w.Header().Set("WWW-Authenticate", `Bearer realm="dhtsearch-api"`)
			writeError(w, http.StatusUnauthorized, "api token required")
			return false
		}
		return true
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.searchTimeout())
	defer cancel()
	tok, err := s.st.APITokenByHash(ctx, apiTokenHash(presented))
	if err != nil {
		s.logger.Printf("api: token lookup: %v", err)
		if require {
			writeError(w, http.StatusServiceUnavailable, "token check unavailable")
			return false
		}
		return true // anonymous mode: a broken lookup must not lock the site
	}
	if tok == nil || subtle.ConstantTimeCompare([]byte(tok.TokenHash), []byte(apiTokenHash(presented))) != 1 {
		if require {
			w.Header().Set("WWW-Authenticate", `Bearer realm="dhtsearch-api"`)
			writeError(w, http.StatusUnauthorized, "invalid api token")
			return false
		}
		return true // not enforced: an unknown token is just anonymous
	}
	if !tok.Enabled {
		if require {
			writeError(w, http.StatusForbidden, "api token disabled")
			return false
		}
		return true
	}
	if require && !s.tokenLimiter.allow(tok.ID, tok.RatePerMin) {
		retry := 60 / max(tok.RatePerMin, 1)
		if retry < 1 {
			retry = 1
		}
		w.Header().Set("Retry-After", strconv.Itoa(retry))
		writeError(w, http.StatusTooManyRequests, "api token rate limit exceeded")
		return false
	}
	if err := s.st.TouchAPIToken(ctx, tok.ID, time.Now().Unix()); err != nil {
		// Counting must never fail a call that already authenticated.
		s.logger.Printf("api: token touch #%d: %v", tok.ID, err)
	}
	return true
}

// --- admin endpoints: /api/admin/api-tokens ---

func (s *Server) handleAdminAPITokenList(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	ctx, cancel := s.adminCtx(r)
	defer cancel()
	items, err := s.st.ListAPITokens(ctx)
	if err != nil {
		s.logger.Printf("api: admin api-token list: %v", err)
		writeError(w, http.StatusInternalServerError, "list failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": items})
}

func (s *Server) handleAdminAPITokenCreate(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var body struct {
		Name       string `json:"name"`
		RatePerMin *int   `json:"rate_per_min"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad request")
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" || len([]rune(name)) > 100 {
		writeError(w, http.StatusBadRequest, "name must be 1..100 characters")
		return
	}
	rate := defaultTokenRate
	if s.opts.Settings != nil {
		rate = s.opts.Settings.Int(settings.KeyPublicAPITokenRatePerMin)
	}
	if body.RatePerMin != nil {
		rate = *body.RatePerMin
	}
	if rate < 0 || rate > maxTokenRate {
		writeError(w, http.StatusBadRequest, "rate_per_min must be 0..1000000")
		return
	}
	plaintext, err := generateAPIToken()
	if err != nil {
		s.logger.Printf("api: admin api-token generate: %v", err)
		writeError(w, http.StatusInternalServerError, "create failed")
		return
	}
	ctx, cancel := s.adminCtx(r)
	defer cancel()
	tok, err := s.st.CreateAPIToken(ctx, name, apiTokenDisplayPrefix(plaintext), apiTokenHash(plaintext), rate, time.Now().Unix())
	if err != nil {
		s.logger.Printf("api: admin api-token create: %v", err)
		writeError(w, http.StatusInternalServerError, "create failed")
		return
	}
	// The audit line carries the prefix only — never the plaintext.
	s.logger.Printf("api: admin: api token created id=%d name=%q prefix=%s rate_per_min=%d", tok.ID, tok.Name, tok.Prefix, tok.RatePerMin)
	writeJSON(w, http.StatusCreated, map[string]any{
		"token": plaintext, // shown exactly once; only its hash is stored
		"item":  tok,
	})
}

// adminAPITokenTarget parses the {id} path value.
func adminAPITokenID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		writeError(w, http.StatusBadRequest, "invalid token id")
		return 0, false
	}
	return id, true
}

func (s *Server) handleAdminAPITokenEnable(w http.ResponseWriter, r *http.Request) {
	s.setAPITokenEnabled(w, r, true)
}

func (s *Server) handleAdminAPITokenDisable(w http.ResponseWriter, r *http.Request) {
	s.setAPITokenEnabled(w, r, false)
}

func (s *Server) setAPITokenEnabled(w http.ResponseWriter, r *http.Request, enabled bool) {
	if !s.requireAdmin(w, r) {
		return
	}
	id, ok := adminAPITokenID(w, r)
	if !ok {
		return
	}
	ctx, cancel := s.adminCtx(r)
	defer cancel()
	found, err := s.st.SetAPITokenEnabled(ctx, id, enabled)
	if err != nil {
		s.logger.Printf("api: admin api-token set-enabled: %v", err)
		writeError(w, http.StatusInternalServerError, "update failed")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	s.logger.Printf("api: admin: api token id=%d enabled=%v", id, enabled)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleAdminAPITokenRate(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id, ok := adminAPITokenID(w, r)
	if !ok {
		return
	}
	var body struct {
		RatePerMin *int `json:"rate_per_min"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil || body.RatePerMin == nil {
		writeError(w, http.StatusBadRequest, "rate_per_min required")
		return
	}
	if *body.RatePerMin < 0 || *body.RatePerMin > maxTokenRate {
		writeError(w, http.StatusBadRequest, "rate_per_min must be 0..1000000")
		return
	}
	ctx, cancel := s.adminCtx(r)
	defer cancel()
	found, err := s.st.SetAPITokenRate(ctx, id, *body.RatePerMin)
	if err != nil {
		s.logger.Printf("api: admin api-token set-rate: %v", err)
		writeError(w, http.StatusInternalServerError, "update failed")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	s.logger.Printf("api: admin: api token id=%d rate_per_min=%d", id, *body.RatePerMin)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleAdminAPITokenDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id, ok := adminAPITokenID(w, r)
	if !ok {
		return
	}
	ctx, cancel := s.adminCtx(r)
	defer cancel()
	found, err := s.st.DeleteAPIToken(ctx, id)
	if err != nil {
		s.logger.Printf("api: admin api-token delete: %v", err)
		writeError(w, http.StatusInternalServerError, "delete failed")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	s.logger.Printf("api: admin: api token id=%d deleted", id)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
