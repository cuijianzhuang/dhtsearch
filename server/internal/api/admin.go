package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strconv"
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
	mux.HandleFunc("POST /api/admin/auth/password", s.handleAdminPasswordLogin)
	mux.HandleFunc("POST /api/admin/auth/github/exchange", s.handleGitHubExchange)
	mux.HandleFunc("POST /api/admin/auth/oidc/exchange", s.handleOIDCExchange)
	mux.HandleFunc("GET /api/admin/auth/config", s.handleAuthConfig)
	mux.HandleFunc("GET /api/admin/me", s.handleAdminMe)
	mux.HandleFunc("POST /api/admin/logout", s.handleAdminLogout)
	mux.HandleFunc("POST /api/admin/me/password", s.handleAdminChangeMyPassword)
	mux.HandleFunc("GET /api/admin/users", s.handleAdminUsersList)
	mux.HandleFunc("POST /api/admin/users", s.handleAdminUsersCreate)
	mux.HandleFunc("POST /api/admin/users/{id}", s.handleAdminUserUpdate)
	mux.HandleFunc("POST /api/admin/users/{id}/password", s.handleAdminUserResetPassword)
	mux.HandleFunc("DELETE /api/admin/users/{id}", s.handleAdminUserDelete)
	mux.HandleFunc("GET /api/admin/overview", s.handleAdminOverview)
	mux.HandleFunc("GET /api/admin/trends", s.handleAdminTrends)
	mux.HandleFunc("GET /api/admin/torrents", s.handleAdminList)
	mux.HandleFunc("POST /api/admin/torrents/batch", s.handleAdminBatch)
	mux.HandleFunc("GET /api/admin/torrents/{hash}", s.handleAdminDetail)
	mux.HandleFunc("GET /api/admin/torrents/{hash}/files", s.handleAdminTorrentFiles)
	mux.HandleFunc("POST /api/admin/torrents/{hash}/block", s.handleAdminBlock)
	mux.HandleFunc("POST /api/admin/torrents/{hash}/approve", s.handleAdminApprove)
	mux.HandleFunc("DELETE /api/admin/torrents/{hash}", s.handleAdminDelete)
	mux.HandleFunc("GET /api/admin/blocked", s.handleAdminBlockedList)
	mux.HandleFunc("DELETE /api/admin/blocked/{hash}", s.handleAdminUnblock)
	mux.HandleFunc("GET /api/admin/settings", s.handleAdminSettingsGet)
	mux.HandleFunc("PUT /api/admin/settings", s.handleAdminSettingsPut)
	mux.HandleFunc("GET /api/admin/update", s.handleAdminUpdateGet)
	mux.HandleFunc("POST /api/admin/update/check", s.handleAdminUpdateCheck)
	mux.HandleFunc("POST /api/admin/update/request", s.handleAdminUpdateRequest)
	mux.HandleFunc("GET /api/admin/restart", s.handleAdminRestartGet)
	mux.HandleFunc("POST /api/admin/restart", s.handleAdminRestartRequest)
	mux.HandleFunc("GET /api/admin/api-tokens", s.handleAdminAPITokenList)
	mux.HandleFunc("POST /api/admin/api-tokens", s.handleAdminAPITokenCreate)
	mux.HandleFunc("POST /api/admin/api-tokens/{id}/enable", s.handleAdminAPITokenEnable)
	mux.HandleFunc("POST /api/admin/api-tokens/{id}/disable", s.handleAdminAPITokenDisable)
	mux.HandleFunc("PUT /api/admin/api-tokens/{id}/rate", s.handleAdminAPITokenRate)
	mux.HandleFunc("DELETE /api/admin/api-tokens/{id}", s.handleAdminAPITokenDelete)
	mux.HandleFunc("GET /api/admin/moderation", s.handleAdminModeration)
	mux.HandleFunc("POST /api/admin/moderation/test", s.handleAdminModerationTest)
	mux.HandleFunc("GET /api/admin/reports", s.handleAdminReports)
	mux.HandleFunc("POST /api/admin/reports/{id}/resolve", s.handleAdminReportResolve)
	mux.HandleFunc("POST /api/admin/reports/{id}/dismiss", s.handleAdminReportDismiss)
	mux.HandleFunc("POST /api/admin/reports/{id}/block", s.handleAdminReportBlock)
}

// handleAdminLogin verifies the ADMIN_TOKEN recovery credential and
// trades it for a server-side session, so the web UI's cookie holds
// a revocable session token rather than the recovery token itself.
// (adminEnabled / requireAdmin live in auth.go, where the principal
// resolution for every credential kind is defined.)
func (s *Server) handleAdminLogin(w http.ResponseWriter, r *http.Request) {
	if s.opts.AdminToken == "" {
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
	ctx, cancel := s.adminCtx(r)
	defer cancel()
	token, err := s.mintSession(ctx, "", "token")
	if err != nil {
		// Session storage is down; the login itself is valid, so
		// answer as before and let the web layer fall back to
		// storing the recovery token (which requireAdmin accepts
		// directly) rather than locking the operator out.
		s.logger.Printf("api: mint token session: %v", err)
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	writeJSON(w, http.StatusOK, loginResult{
		OK:      true,
		Session: token,
		Principal: &principal{
			Kind: "token", Username: "ADMIN_TOKEN", Role: roleAdmin, Method: "token",
		},
	})
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
	reportsPending, err := s.st.PendingReportsCount(ctx)
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
		"torrents": total,
		// stored_total is the same Count as "torrents", named for the
		// pipeline hero: its 入库 stage displays the library's actual
		// current size (identical to the 种子总数 KPI), not a value
		// derived from the cumulative stats counters — the old
		// fetched − filtered derivation drifted above the real total
		// because a re-fetch of a known hash increments "fetched" on
		// every upsert while deletions never decrement anything.
		"stored_total":    total,
		"today":           today,
		"pending_review":  pending,
		"reviewed":        total - pending,
		"blocked":         blocked,
		"reports_pending": reportsPending,
		"dialect":         s.st.Dialect(),
		"stats":           counters,
		"fetch": map[string]any{
			"fetched":           counters["fetched"],
			"timed_out":         counters["meta_timed_out"],
			"failed":            counters["meta_failed"],
			"prescreen_dropped": counters["prescreen_dropped"],
			// Admission refusals (internal/admission): hashes the
			// queue never spent a worker on, split by reason.
			"skipped_stored":  counters["admission_skipped_stored"],
			"skipped_backoff": counters["admission_skipped_backoff"],
			// Peer hints: fetches that carried DHT peer hints, and
			// of those the successes with a hint peer connected.
			"peer_hint_items":   counters["peer_hint_items"],
			"peer_hint_fetched": counters["peer_hint_fetched"],
		},
		"moderation": map[string]any{
			"enabled":       s.moderationEnabled(),
			"reviewed":      counters["llm_reviewed"],
			"adult_removed": counters["llm_adult_removed"],
			"spam_removed":  counters["llm_spam_removed"],
			"errors":        counters["llm_errors"],
			"pending":       pending,
			"blocked":       blocked,
		},
	}
	s.metaStatusInto(resp["fetch"].(map[string]any))
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

// moderationEnabled reports whether the LLM moderation pass can
// actually run: the same gate the moderator itself applies (feature
// switch on AND an API key present), read from the live settings
// registry. Without a registry (bare test wiring) the pass cannot be
// configured, so it reports false; the console uses this to show a
// "not enabled" state instead of a wall of zeros.
func (s *Server) moderationEnabled() bool {
	if s.opts.Settings == nil {
		return false
	}
	return s.opts.Settings.Bool(settings.KeyModerationEnabled) &&
		s.opts.Settings.Get(settings.KeyOpenAIAPIKey) != ""
}

type trendBucket struct {
	Start int64 `json:"start"`
	Count int64 `json:"count"`
}

// handleAdminTrends serves the ingest time series behind the console's
// overview chart: torrent counts per hour over the last 24 hours
// (range=24h, the default) or per day over the last 14 days
// (range=14d). Windows align to the server's local hour / midnight so
// the last bucket is always the current, still-filling period, and
// empty buckets are materialised as zeros — the frontend draws the
// series verbatim.
func (s *Server) handleAdminTrends(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	now := time.Now()
	loc := now.Location()
	var start time.Time
	var bucketSec int64
	var buckets int
	rng := r.URL.Query().Get("range")
	switch rng {
	case "", "24h":
		rng = "24h"
		bucketSec, buckets = 3600, 24
		start = time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, loc).
			Add(-time.Duration(buckets-1) * time.Hour)
	case "14d":
		bucketSec, buckets = 86400, 14
		start = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).
			AddDate(0, 0, -(buckets - 1))
	default:
		writeError(w, http.StatusBadRequest, "invalid range")
		return
	}
	ctx, cancel := s.adminCtx(r)
	defer cancel()
	counts, err := s.st.CreatedBuckets(ctx, start.Unix(), bucketSec, buckets)
	if err != nil {
		s.logger.Printf("api: admin trends: %v", err)
		writeError(w, http.StatusInternalServerError, "trends failed")
		return
	}
	out := make([]trendBucket, buckets)
	for i := range out {
		out[i] = trendBucket{
			Start: start.Add(time.Duration(i) * time.Duration(bucketSec) * time.Second).Unix(),
			Count: counts[i],
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"range":          rng,
		"bucket_seconds": bucketSec,
		"buckets":        out,
	})
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

func toAdminItem(t store.AdminTorrent, trackers []string) adminItem {
	return adminItem{
		InfoHash:   t.InfoHash,
		Name:       t.Name,
		RawName:    t.RawName,
		Alias:      t.Alias,
		TotalSize:  t.TotalSize,
		FileCount:  t.FileCount,
		CreatedAt:  t.CreatedAt,
		ReviewedAt: t.ReviewedAt,
		Magnet:     magnetURI(t.InfoHash, t.Name, trackers),
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
	trackers := s.trackers()
	out := make([]adminItem, 0, len(items))
	for _, t := range items {
		out = append(out, toAdminItem(t, trackers))
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
	// Metadata only: the file list is paged separately through
	// GET /api/admin/torrents/{hash}/files. (This response used to
	// embed every stored file — ~300 KB for a 5000-file torrent.)
	t, err := s.st.AdminGetMeta(ctx, r.PathValue("hash"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "detail failed")
		return
	}
	if t == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	resp := map[string]any{
		"info_hash":   t.InfoHash,
		"name":        t.Name,
		"raw_name":    t.RawName,
		"clean_name":  t.CleanName,
		"alias":       t.Alias,
		"total_size":  t.TotalSize,
		"file_count":  t.FileCount,
		"created_at":  t.CreatedAt,
		"reviewed_at": t.ReviewedAt,
		"magnet":      magnetURI(t.InfoHash, t.Name, s.trackers()),
	}
	// Swarm snapshot, same wire shape as the public search results:
	// the counters are null when no tracker has ever reported on
	// this hash, and health carries the derived label either way.
	// A lookup failure degrades to the null shape — swarm data is
	// advisory and must not fail the detail view.
	var swp *store.SwarmStats
	if m, err := s.st.SwarmStatsFor(ctx, []string{t.InfoHash}); err == nil {
		if v, ok := m[t.InfoHash]; ok {
			vv := v
			swp = &vv
		}
	}
	swarmField := func(get func(*store.SwarmStats) int64) any {
		if swp == nil {
			return nil
		}
		return get(swp)
	}
	resp["seeders"] = swarmField(func(s *store.SwarmStats) int64 { return s.Seeders })
	resp["leechers"] = swarmField(func(s *store.SwarmStats) int64 { return s.Leechers })
	resp["completed"] = swarmField(func(s *store.SwarmStats) int64 { return s.Completed })
	resp["scraped_at"] = swarmField(func(s *store.SwarmStats) int64 { return s.ScrapedAt })
	resp["zero_since"] = swarmField(func(s *store.SwarmStats) int64 { return s.ZeroSince })
	resp["zero_count"] = swarmField(func(s *store.SwarmStats) int64 { return s.ZeroCount })
	resp["health"] = store.SwarmHealth(swp)
	writeJSON(w, http.StatusOK, resp)
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
	case "delete":
		// Plain removal (no blocked-table record), the batch twin of
		// the single-torrent DELETE the console already exposes.
		var n int64
		for _, h := range body.Hashes {
			deleted, err := s.st.AdminDelete(ctx, h)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "delete failed")
				return
			}
			if deleted {
				n++
			}
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

// --- Visitor report queue ---

// handleAdminReports lists the report queue. The status filter is
// pending (the default worklist), resolved, dismissed or all.
func (s *Server) handleAdminReports(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	status := store.ReportPending
	switch r.URL.Query().Get("status") {
	case "", "pending":
		// default worklist
	case "resolved":
		status = store.ReportResolved
	case "dismissed":
		status = store.ReportDismissed
	case "all":
		status = store.ReportStatusAll
	default:
		writeError(w, http.StatusBadRequest, "invalid status")
		return
	}
	page, pageSize := adminPage(r)
	ctx, cancel := s.adminCtx(r)
	defer cancel()
	items, total, err := s.st.AdminListReports(ctx, status, page, pageSize)
	if err != nil {
		s.logger.Printf("api: admin reports: %v", err)
		writeError(w, http.StatusInternalServerError, "list failed")
		return
	}
	if items == nil {
		items = []store.Report{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"total":     total,
		"page":      page,
		"page_size": pageSize,
		"results":   items,
	})
}

// adminReport resolves the {id} path value to a stored report,
// answering 400/404 itself when it does not parse or exist.
func (s *Server) adminReport(w http.ResponseWriter, r *http.Request) *store.Report {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		writeError(w, http.StatusBadRequest, "invalid id")
		return nil
	}
	ctx, cancel := s.adminCtx(r)
	defer cancel()
	rep, err := s.st.ReportByID(ctx, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "lookup failed")
		return nil
	}
	if rep == nil {
		writeError(w, http.StatusNotFound, "not found")
		return nil
	}
	return rep
}

func (s *Server) setReportStatus(w http.ResponseWriter, r *http.Request, status int) {
	rep := s.adminReport(w, r)
	if rep == nil {
		return
	}
	ctx, cancel := s.adminCtx(r)
	defer cancel()
	ok, err := s.st.AdminSetReportStatus(ctx, rep.ID, status)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "update failed")
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleAdminReportResolve(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	s.setReportStatus(w, r, store.ReportResolved)
}

func (s *Server) handleAdminReportDismiss(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	s.setReportStatus(w, r, store.ReportDismissed)
}

// handleAdminReportBlock is the queue's one-click takedown: block the
// reported torrent through the same Block path the console uses (so
// the crawler cannot re-add it) and mark the report resolved. When
// the torrent is already gone the report is still resolved — there is
// nothing left to take down.
func (s *Server) handleAdminReportBlock(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	rep := s.adminReport(w, r)
	if rep == nil {
		return
	}
	ctx, cancel := s.adminCtx(r)
	defer cancel()
	var deleted int64
	if t, err := s.st.AdminGet(ctx, rep.InfoHash); err != nil {
		writeError(w, http.StatusInternalServerError, "block failed")
		return
	} else if t != nil {
		n, err := s.st.Block([]string{t.InfoHash}, []string{t.RawName},
			"report:"+rep.Reason, time.Now().Unix())
		if err != nil {
			s.logger.Printf("api: admin report block: %v", err)
			writeError(w, http.StatusInternalServerError, "block failed")
			return
		}
		deleted = n
	}
	if _, err := s.st.AdminSetReportStatus(ctx, rep.ID, store.ReportResolved); err != nil {
		writeError(w, http.StatusInternalServerError, "update failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": deleted})
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
