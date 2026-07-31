package admin

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"dhtsearch/server/internal/settings"
	"dhtsearch/server/internal/store"
)

// Config wires the console to the running server.
type Config struct {
	// Password enables the console. Empty leaves it switched off entirely,
	// which is the default: a console reachable without a password would be
	// strictly worse than no console.
	Password string
	// Secure marks cookies Secure. Required when the site is served over
	// HTTPS, which it should be — the password crosses the wire on login.
	Secure bool
	// Settings is the live configuration the console edits.
	Settings *settings.Settings
	// Stats renders the same payload as /api/stats, reused so the dashboard
	// and the public endpoint can never disagree.
	Stats func(ctx context.Context) ([]byte, error)
	// SweepNow triggers a moderation pass out of band. Nil when moderation is
	// not running, which the console reports rather than pretending.
	SweepNow func(ctx context.Context) (reviewed int, deleted int64, err error)
	// RestartOnly is reported next to the live settings so the operator can
	// see the knobs the console deliberately does not offer.
	RestartOnly map[string]string
	// BaseCtx bounds background work started by the console. A manual sweep
	// outlives the request that asked for it, so it cannot use the request's
	// context — that one is cancelled as soon as the response is written.
	// This should be the server's lifetime context so a sweep still stops on
	// shutdown. Nil falls back to context.Background.
	BaseCtx context.Context
	Logger  *log.Logger
}

// sweepState tracks the manual moderation sweep, which runs in the background
// and is reported through /api/admin/state.
type sweepState struct {
	mu       sync.Mutex
	running  bool
	started  time.Time
	finished time.Time
	reviewed int
	deleted  int64
	failure  string
}

func (s *sweepState) snapshot() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]any{"running": s.running}
	if !s.started.IsZero() {
		out["started_at"] = s.started.Unix()
	}
	if !s.finished.IsZero() {
		out["finished_at"] = s.finished.Unix()
		out["reviewed"] = s.reviewed
		out["deleted"] = s.deleted
		if s.failure != "" {
			out["error"] = s.failure
		}
	}
	return out
}

// Server is the admin console.
type Server struct {
	cfg   Config
	st    *store.Store
	auth  *auth
	sweep sweepState
}

// New builds the console. It returns nil when no password is configured,
// which callers treat as "do not mount the routes".
func New(st *store.Store, cfg Config) *Server {
	if cfg.Password == "" {
		return nil
	}
	if cfg.Logger == nil {
		cfg.Logger = log.Default()
	}
	if cfg.BaseCtx == nil {
		cfg.BaseCtx = context.Background()
	}
	return &Server{cfg: cfg, st: st, auth: newAuth(cfg.Password, cfg.Secure)}
}

// Mount registers the console's routes on mux.
func (s *Server) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin", s.handlePage)
	mux.HandleFunc("POST /api/admin/login", s.handleLogin)
	mux.HandleFunc("POST /api/admin/logout", s.handleLogout)
	mux.HandleFunc("GET /api/admin/state", s.guard(false, s.handleState))
	mux.HandleFunc("GET /api/admin/blocked", s.guard(false, s.handleBlocked))
	mux.HandleFunc("POST /api/admin/settings", s.guard(true, s.handleSetSetting))
	mux.HandleFunc("POST /api/admin/torrent/delete", s.guard(true, s.handleDelete))
	mux.HandleFunc("POST /api/admin/unblock", s.guard(true, s.handleUnblock))
	mux.HandleFunc("POST /api/admin/unblock-reason", s.guard(true, s.handleUnblockReason))
	mux.HandleFunc("POST /api/admin/reset-reviewed", s.guard(true, s.handleResetReviewed))
	mux.HandleFunc("POST /api/admin/sweep", s.guard(true, s.handleSweep))
}

// guard wraps a handler in the session and CSRF checks.
func (s *Server) guard(mutating bool, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.auth.authorize(r, mutating); !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		h(w, r)
	}
}

// audit records a mutation with the client it came from. Every write the
// console can perform is irreversible from the UI's point of view, so the log
// is the only account of who changed what.
func (s *Server) audit(r *http.Request, format string, args ...any) {
	s.cfg.Logger.Printf("admin[%s]: "+format, append([]any{clientIP(r)}, args...)...)
}

// clientIP is the throttling and audit key. It trusts forwarding headers only
// from loopback and private peers — the reverse proxy in front of this
// process — so a public client cannot spread its login attempts across forged
// addresses to dodge the lockout.
func clientIP(r *http.Request) string {
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	host = strings.Trim(host, "[]")
	if !isTrustedPeer(host) {
		return host
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		for i := len(parts) - 1; i >= 0; i-- {
			p := strings.TrimSpace(parts[i])
			if p != "" && !isTrustedPeer(p) {
				return p
			}
		}
	}
	if xr := strings.TrimSpace(r.Header.Get("X-Real-IP")); xr != "" {
		return xr
	}
	return host
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if wait, locked := s.auth.lockedOut(ip); locked {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeJSON(w, http.StatusTooManyRequests, map[string]string{
			"error": "too many failed attempts, try again in " + wait.Round(time.Second).String(),
		})
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	if !s.auth.checkPassword(body.Password) {
		s.auth.noteFailure(ip)
		s.cfg.Logger.Printf("admin[%s]: failed login", ip)
		// Deliberately vague: naming what was wrong helps only an attacker.
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "密码错误"})
		return
	}
	token, csrf := s.auth.startSession(ip)
	s.auth.setCookies(w, token, csrf)
	s.cfg.Logger.Printf("admin[%s]: login", ip)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.auth.endSession(c.Value)
	}
	s.auth.clearCookies(w)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	stats := json.RawMessage("{}")
	if s.cfg.Stats != nil {
		body, err := s.cfg.Stats(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "stats failed"})
			return
		}
		stats = body
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"stats":        stats,
		"settings":     s.cfg.Settings.Defs(),
		"restart_only": s.cfg.RestartOnly,
		"can_sweep":    s.cfg.SweepNow != nil,
		"sweep":        s.sweep.snapshot(),
	})
}

func (s *Server) handleBlocked(w http.ResponseWriter, r *http.Request) {
	reason := r.URL.Query().Get("reason")
	if reason != "" && reason != "adult" && reason != "spam" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad reason"})
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	entries, err := s.st.ListBlocked(r.Context(), reason, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query failed"})
		return
	}
	if entries == nil {
		entries = []store.BlockedEntry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"blocked": entries})
}

func (s *Server) handleSetSetting(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	if err := s.cfg.Settings.Set(body.Key, body.Value); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.audit(r, "set %s = %s", body.Key, body.Value)
	writeJSON(w, http.StatusOK, map[string]any{"settings": s.cfg.Settings.Defs()})
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	var body struct {
		InfoHash  string `json:"info_hash"`
		Blocklist bool   `json:"blocklist"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	hash := strings.ToLower(strings.TrimSpace(body.InfoHash))
	if !validInfohash(hash) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "info_hash 必须是 40 位十六进制"})
		return
	}
	deleted, err := s.st.Delete(hash, "admin", body.Blocklist, time.Now().Unix())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "delete failed"})
		return
	}
	s.audit(r, "delete %s (blocklist=%v, existed=%v)", hash, body.Blocklist, deleted)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": deleted})
}

func (s *Server) handleUnblock(w http.ResponseWriter, r *http.Request) {
	var body struct {
		InfoHash string `json:"info_hash"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	hash := strings.ToLower(strings.TrimSpace(body.InfoHash))
	if !validInfohash(hash) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "info_hash 必须是 40 位十六进制"})
		return
	}
	ok, err := s.st.Unblock(hash)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "unblock failed"})
		return
	}
	s.audit(r, "unblock %s (found=%v)", hash, ok)
	writeJSON(w, http.StatusOK, map[string]any{"unblocked": ok})
}

func (s *Server) handleUnblockReason(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Reason string `json:"reason"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	if body.Reason != "adult" && body.Reason != "spam" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "reason 必须是 adult 或 spam"})
		return
	}
	n, err := s.st.UnblockReason(body.Reason)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "unblock failed"})
		return
	}
	s.audit(r, "unblock all reason=%s (%d rows)", body.Reason, n)
	writeJSON(w, http.StatusOK, map[string]any{"unblocked": n})
}

func (s *Server) handleResetReviewed(w http.ResponseWriter, r *http.Request) {
	n, err := s.st.ResetReviewed()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "reset failed"})
		return
	}
	s.audit(r, "reset reviewed_at (%d rows will be re-classified)", n)
	writeJSON(w, http.StatusOK, map[string]any{"reset": n})
}

func (s *Server) handleSweep(w http.ResponseWriter, r *http.Request) {
	if s.cfg.SweepNow == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "审核未启用（缺少 OPENAI_API_KEY）"})
		return
	}
	s.sweep.mu.Lock()
	if s.sweep.running {
		s.sweep.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{"error": "已有一轮审核在进行中"})
		return
	}
	s.sweep.running = true
	s.sweep.started = time.Now()
	s.sweep.finished = time.Time{}
	s.sweep.failure = ""
	s.sweep.mu.Unlock()

	// The sweep runs detached from this request. A pass of MaxBatches batches
	// can take many minutes while the server's WriteTimeout is 30 seconds, so
	// answering synchronously would have the console report a network failure
	// on a sweep that is in fact still running — and invite a retry that pays
	// the model twice. Report acceptance now; progress comes from /state.
	s.audit(r, "manual moderation sweep started")
	go func() {
		reviewed, deleted, err := s.cfg.SweepNow(s.cfg.BaseCtx)
		s.sweep.mu.Lock()
		s.sweep.running = false
		s.sweep.finished = time.Now()
		s.sweep.reviewed, s.sweep.deleted = reviewed, deleted
		if err != nil {
			s.sweep.failure = err.Error()
		}
		s.sweep.mu.Unlock()
		if err != nil {
			s.cfg.Logger.Printf("admin: manual sweep: %v", err)
			return
		}
		s.cfg.Logger.Printf("admin: manual sweep done: reviewed=%d deleted=%d", reviewed, deleted)
	}()
	writeJSON(w, http.StatusAccepted, map[string]any{"started": true})
}

// decodeBody reads a bounded JSON body.
func decodeBody(w http.ResponseWriter, r *http.Request, v any) error {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		return errors.New("expected application/json")
	}
	return json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(v)
}

// validInfohash reports whether s is 40 hex characters.
func validInfohash(s string) bool {
	if len(s) != 40 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	// The console is not a public resource and must never be cached by a
	// proxy: its responses are per-session.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}
