package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"dhtsearch/server/internal/settings"
	"dhtsearch/server/internal/store"
)

// Admin API (/api/admin/*): the operator console backend. Every route is
// behind the ADMIN_TOKEN bearer check; with no token configured the
// whole surface answers 503 so a deployment that never opted in exposes
// nothing. Login attempts get their own, much stricter limiter than the
// public API's — the token is the only credential, so guessing must be
// slow even from an address the general limiter would still serve.
const (
	adminLoginRate  = 0.1 // 6 attempts/minute sustained per client
	adminLoginBurst = 5
	maxAdminBatch   = 100
)

func (s *Server) registerAdminRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/admin/login", s.handleAdminLogin)
	mux.HandleFunc("GET /api/admin/overview", s.handleAdminOverview)
	mux.HandleFunc("GET /api/admin/torrents", s.handleAdminList)
	mux.HandleFunc("POST /api/admin/torrents/batch", s.handleAdminBatch)
	mux.HandleFunc("GET /api/admin/torrents/{hash}", s.handleAdminDetail)
	mux.HandleFunc("POST /api/admin/torrents/{hash}/block", s.handleAdminBlock)
	mux.HandleFunc("POST /api/admin/torrents/{hash}/approve", s.handleAdminApprove)
	mux.HandleFunc("DELETE /api/admin/torrents/{hash}", s.handleAdminDelete)
	mux.HandleFunc("GET /api/admin/blocked", s.handleAdminBlockedList)
	mux.HandleFunc("DELETE /api/admin/blocked/{hash}", s.handleAdminUnblock)
	mux.HandleFunc("GET /api/admin/settings", s.handleAdminSettingsGet)
	mux.HandleFunc("PUT /api/admin/settings", s.handleAdminSettingsPut)
	mux.HandleFunc("GET /api/admin/api-tokens", s.handleAdminAPITokenList)
	mux.HandleFunc("POST /api/admin/api-tokens", s.handleAdminAPITokenCreate)
	mux.HandleFunc("POST /api/admin/api-tokens/{id}/enable", s.handleAdminAPITokenEnable)
	mux.HandleFunc("POST /api/admin/api-tokens/{id}/disable", s.handleAdminAPITokenDisable)
	mux.HandleFunc("PUT /api/admin/api-tokens/{id}/rate", s.handleAdminAPITokenRate)
	mux.HandleFunc("DELETE /api/admin/api-tokens/{id}", s.handleAdminAPITokenDelete)
	mux.HandleFunc("GET /api/admin/moderation", s.handleAdminModeration)
	mux.HandleFunc("POST /api/admin/moderation/test", s.handleAdminModerationTest)
}

func (s *Server) adminEnabled() bool { return s.opts.AdminToken != "" }

// requireAdmin gates a handler: 503 when the admin API is disabled, 401
// when the bearer token is missing or wrong. Returns false when it has
// already answered the request.
func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if !s.adminEnabled() {
		writeError(w, http.StatusServiceUnavailable, "admin disabled")
		return false
	}
	tok := ""
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		tok = strings.TrimPrefix(auth, "Bearer ")
	}
	if tok == "" || subtle.ConstantTimeCompare([]byte(tok), []byte(s.opts.AdminToken)) != 1 {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return false
	}
	return true
}

// handleAdminLogin verifies a token without exposing any data, so the
// web UI can trade the operator's token for an httpOnly cookie only
// after the backend has accepted it.
func (s *Server) handleAdminLogin(w http.ResponseWriter, r *http.Request) {
	if !s.adminEnabled() {
		writeError(w, http.StatusServiceUnavailable, "admin disabled")
		return
	}
	if !s.loginLimiter.allow(clientKey(r)) {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "too many login attempts")
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	if err := dec.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad request")
		return
	}
	if body.Token == "" || subtle.ConstantTimeCompare([]byte(body.Token), []byte(s.opts.AdminToken)) != 1 {
		writeError(w, http.StatusUnauthorized, "invalid token")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) adminCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), s.searchTimeout())
}

// handleAdminOverview serves the console's landing numbers: library
// totals, today's additions, the moderation backlog, the pipeline
// counters and the live crawler/scraper/feeds status (the same values
// /api/stats publishes, plus the counts only the store can answer).
func (s *Server) handleAdminOverview(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	ctx, cancel := s.adminCtx(r)
	defer cancel()

	total, err := s.st.Count(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "overview failed")
		return
	}
	now := time.Now()
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Unix()
	today, err := s.st.CountCreatedSince(ctx, midnight)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "overview failed")
		return
	}
	pending, err := s.st.PendingReviewCount(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "overview failed")
		return
	}
	blocked, err := s.st.BlockedCount(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "overview failed")
		return
	}
	counters, err := s.st.Stats(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "overview failed")
		return
	}
	resp := map[string]any{
		"torrents":       total,
		"today":          today,
		"pending_review": pending,
		"reviewed":       total - pending,
		"blocked":        blocked,
		"dialect":        s.st.Dialect(),
		"stats":          counters,
		"fetch": map[string]any{
			"fetched":           counters["fetched"],
			"timed_out":         counters["meta_timed_out"],
			"failed":            counters["meta_failed"],
			"prescreen_dropped": counters["prescreen_dropped"],
		},
		"moderation": map[string]any{
			"reviewed":      counters["llm_reviewed"],
			"adult_removed": counters["llm_adult_removed"],
			"spam_removed":  counters["llm_spam_removed"],
			"errors":        counters["llm_errors"],
			"pending":       pending,
			"blocked":       blocked,
		},
	}
	if s.opts.MetaStatus != nil {
		resp["fetch"].(map[string]any)["queue_depth"] = s.opts.MetaStatus().QueueDepth
	}
	// Static-filter state: the category switches as currently
	// configured (a category that is off no longer increments its
	// counter, so the counts below freeze where they were turned off)
	// next to the cumulative counters themselves.
	if s.opts.Settings != nil {
		mgr := s.opts.Settings
		resp["filter"] = map[string]any{
			"enabled":           mgr.Bool(settings.KeyFilterEnabled),
			"adult_enabled":     mgr.Bool(settings.KeyFilterAdultEnabled),
			"spam_enabled":      mgr.Bool(settings.KeyFilterSpamEnabled),
			"too_small_enabled": mgr.Bool(settings.KeyFilterTooSmallEnabled),
			"adult_filtered":    counters["adult_filtered"],
			"spam_filtered":     counters["spam_filtered"],
			"size_filtered":     counters["size_filtered"],
		}
	}
	if s.crawler != nil {
		resp["crawler"] = s.crawler()
	}
	if s.opts.ScraperStatus != nil {
		resp["scraper"] = s.opts.ScraperStatus()
	}
	if s.opts.FeedsStatus != nil {
		resp["feeds"] = s.opts.FeedsStatus()
	}
	if s.opts.Trending != nil {
		t := s.opts.Trending()
		sec := map[string]any{
			"movies":     len(t.Movies),
			"tv":         len(t.TV),
			"tv_jp":      len(t.TVJP),
			"tv_kr":      len(t.TVKR),
			"updated_at": t.UpdatedAt,
		}
		if s.opts.TrendingStatus != nil {
			ts := s.opts.TrendingStatus()
			sec["failing"] = ts.Failing
			sec["fail_streak"] = ts.FailStreak
			sec["last_success"] = ts.LastSuccess
		}
		resp["trending"] = sec
	}
	writeJSON(w, http.StatusOK, resp)
}

type adminItem struct {
	InfoHash   string `json:"info_hash"`
	Name       string `json:"name"`
	RawName    string `json:"raw_name"`
	Alias      string `json:"alias,omitempty"`
	TotalSize  int64  `json:"total_size"`
	FileCount  int    `json:"file_count"`
	CreatedAt  int64  `json:"created_at"`
	ReviewedAt int64  `json:"reviewed_at"`
	Magnet     string `json:"magnet"`
}

func toAdminItem(t store.AdminTorrent) adminItem {
	return adminItem{
		InfoHash:   t.InfoHash,
		Name:       t.Name,
		RawName:    t.RawName,
		Alias:      t.Alias,
		TotalSize:  t.TotalSize,
		FileCount:  t.FileCount,
		CreatedAt:  t.CreatedAt,
		ReviewedAt: t.ReviewedAt,
		Magnet:     magnetURI(t.InfoHash, t.Name),
	}
}

// adminPage parses and clamps the shared pagination parameters the same
// way handleSearch does.
func adminPage(r *http.Request) (page, pageSize int) {
	page = atoiDefault(r.URL.Query().Get("page"), 1)
	if page < 1 {
		page = 1
	}
	pageSize = atoiDefault(r.URL.Query().Get("page_size"), defaultPerPage)
	if pageSize < 1 {
		pageSize = defaultPerPage
	}
	if pageSize > maxPageSize {
		pageSize = maxPageSize
	}
	if (page-1)*pageSize > maxOffset {
		page = maxOffset/pageSize + 1
	}
	return page, pageSize
}

func (s *Server) handleAdminList(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	status := r.URL.Query().Get("status")
	switch status {
	case "", "all":
		status = store.AdminStatusAll
	case store.AdminStatusUnreviewed, store.AdminStatusReviewed:
		// ok
	default:
		writeError(w, http.StatusBadRequest, "invalid status")
		return
	}
	q := truncateUTF8(r.URL.Query().Get("q"), maxQueryLen)
	page, pageSize := adminPage(r)
	ctx, cancel := s.adminCtx(r)
	defer cancel()
	items, total, err := s.st.AdminList(ctx, q, status, page, pageSize)
	if err != nil {
		s.logger.Printf("api: admin list: %v", err)
		writeError(w, http.StatusInternalServerError, "list failed")
		return
	}
	out := make([]adminItem, 0, len(items))
	for _, t := range items {
		out = append(out, toAdminItem(t))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"total":     total,
		"page":      page,
		"page_size": pageSize,
		"results":   out,
	})
}

func (s *Server) handleAdminDetail(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	ctx, cancel := s.adminCtx(r)
	defer cancel()
	t, err := s.st.AdminGet(ctx, r.PathValue("hash"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "detail failed")
		return
	}
	if t == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"info_hash":   t.InfoHash,
		"name":        t.Name,
		"raw_name":    t.RawName,
		"clean_name":  t.CleanName,
		"alias":       t.Alias,
		"total_size":  t.TotalSize,
		"file_count":  t.FileCount,
		"files":       t.Files,
		"created_at":  t.CreatedAt,
		"reviewed_at": t.ReviewedAt,
		"magnet":      magnetURI(t.InfoHash, t.Name),
	})
}

// adminTarget resolves the {hash} path value to a stored torrent,
// answering 404 itself when it does not exist.
func (s *Server) adminTarget(w http.ResponseWriter, r *http.Request) *store.AdminTorrent {
	ctx, cancel := s.adminCtx(r)
	defer cancel()
	t, err := s.st.AdminGet(ctx, r.PathValue("hash"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "lookup failed")
		return nil
	}
	if t == nil {
		writeError(w, http.StatusNotFound, "not found")
		return nil
	}
	return t
}

func (s *Server) handleAdminBlock(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	t := s.adminTarget(w, r)
	if t == nil {
		return
	}
	reason := "admin"
	var body struct {
		Reason string `json:"reason"`
	}
	if r.Body != nil {
		// Body is optional; a malformed one is ignored in favour of the
		// default reason rather than failing the block.
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body)
		if body.Reason != "" {
			reason = truncateUTF8(body.Reason, 100)
		}
	}
	n, err := s.st.Block([]string{t.InfoHash}, []string{t.RawName}, reason, time.Now().Unix())
	if err != nil {
		s.logger.Printf("api: admin block: %v", err)
		writeError(w, http.StatusInternalServerError, "block failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": n})
}

func (s *Server) handleAdminApprove(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	t := s.adminTarget(w, r)
	if t == nil {
		return
	}
	if _, err := s.st.MarkReviewed([]string{t.InfoHash}, time.Now().Unix()); err != nil {
		writeError(w, http.StatusInternalServerError, "approve failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleAdminDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	ctx, cancel := s.adminCtx(r)
	defer cancel()
	deleted, err := s.st.AdminDelete(ctx, r.PathValue("hash"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "delete failed")
		return
	}
	if !deleted {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleAdminBatch(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var body struct {
		Hashes []string `json:"hashes"`
		Action string   `json:"action"`
		Reason string   `json:"reason"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad request")
		return
	}
	if len(body.Hashes) == 0 || len(body.Hashes) > maxAdminBatch {
		writeError(w, http.StatusBadRequest, "hashes must contain 1..100 entries")
		return
	}
	ctx, cancel := s.adminCtx(r)
	defer cancel()
	switch body.Action {
	case "approve":
		n, err := s.st.MarkReviewed(body.Hashes, time.Now().Unix())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "approve failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "affected": n})
	case "block":
		reason := body.Reason
		if reason == "" {
			reason = "admin"
		}
		var hashes, names []string
		for _, h := range body.Hashes {
			t, err := s.st.AdminGet(ctx, h)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "block failed")
				return
			}
			if t != nil {
				hashes = append(hashes, t.InfoHash)
				names = append(names, t.RawName)
			}
		}
		n, err := s.st.Block(hashes, names, truncateUTF8(reason, 100), time.Now().Unix())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "block failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "affected": n})
	default:
		writeError(w, http.StatusBadRequest, "invalid action")
	}
}

func (s *Server) handleAdminBlockedList(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	page, pageSize := adminPage(r)
	ctx, cancel := s.adminCtx(r)
	defer cancel()
	items, total, err := s.st.AdminBlockedList(ctx, page, pageSize)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list failed")
		return
	}
	if items == nil {
		items = []store.BlockedEntry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"total":     total,
		"page":      page,
		"page_size": pageSize,
		"results":   items,
	})
}

func (s *Server) handleAdminUnblock(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	ctx, cancel := s.adminCtx(r)
	defer cancel()
	removed, err := s.st.AdminUnblock(ctx, r.PathValue("hash"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unblock failed")
		return
	}
	if !removed {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
