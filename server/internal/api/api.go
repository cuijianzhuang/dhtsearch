// Package api exposes the search HTTP API (JSON + CORS) and, when an
// admin token is configured, the token-guarded /api/admin/* console API.
package api

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"dhtsearch/server/internal/feeds"
	"dhtsearch/server/internal/settings"
	"dhtsearch/server/internal/store"
)

const (
	maxQueryLen    = 200
	maxPageSize    = 100
	defaultPerPage = 20
	// maxOffset caps how deep pagination can reach: OFFSET n makes SQLite
	// walk and discard n rows, so an unbounded page number turns one GET
	// into a full-table scan. Nothing legitimate paginates 10k results deep.
	maxOffset = 10_000
	// admissionWait is how long a search waits for an in-flight slot before
	// being turned away. Long enough to absorb a burst, short enough that a
	// flood fails fast instead of stacking goroutines.
	admissionWait = time.Second

	defaultMaxInflight   = 16
	defaultSearchTimeout = 10 * time.Second
	defaultStatsTTL      = 10 * time.Second
)

// Options tunes the API's DoS defenses. Zero values pick safe defaults,
// except RateRPS where <= 0 disables per-client rate limiting entirely
// (tests, fully trusted networks).
type Options struct {
	// RateRPS is the sustained per-client request rate; RateBurst is the
	// bucket size (<= 0: 10 seconds' worth of RateRPS).
	RateRPS   float64
	RateBurst int
	// MaxInflight caps concurrent search queries. The store has one SQLite
	// connection, so extra concurrency only queues; this bounds the queue.
	MaxInflight int
	// SearchTimeout bounds one request's total database time.
	SearchTimeout time.Duration
	// StatsTTL is how long stats and total-count responses are served from
	// cache. Stats hit four aggregate queries; caching makes them O(1).
	StatsTTL time.Duration
	// ScraperStatus, when non-nil, adds a "scraper" section to /api/stats.
	ScraperStatus func() ScraperStatus
	// MetaStatus, when non-nil, adds live metadata-pipeline fields
	// (the fetch priority queue's depth) to the "fetch" section.
	MetaStatus func() MetaStatus
	// FeedsStatus, when non-nil, adds a "feeds" section to /api/stats.
	FeedsStatus func() feeds.Stats
	// Trending, when non-nil, backs /api/trending. Nil (feature disabled)
	// makes the endpoint serve empty lists so the frontend degrades quietly.
	Trending func() Trending
	// TrendingStatus, when non-nil, adds refresh-health fields to
	// the admin overview's trending section, so a fetcher stuck
	// failing (e.g. Douban unreachable since a restart) is visible
	// instead of silently serving stale or empty lists.
	TrendingStatus func() TrendingStatus
	// AdminToken is the bearer token guarding /api/admin/*. Empty (the
	// default) disables the admin API entirely: every admin route answers
	// 503 and the startup log says so. The token comes from the
	// environment only — never stored, never logged.
	AdminToken string
	// Settings, when non-nil, is the live configuration registry: it
	// backs /api/site and /api/admin/settings, and the DoS knobs above
	// (RateRPS, RateBurst, MaxInflight, SearchTimeout) are re-read
	// from it whenever its version moves, so admin edits apply
	// without a restart. The static fields remain the fallback for
	// tests and settings-free wiring.
	Settings *settings.Manager
}

// Trending is the payload for /api/trending: Douban's current hot movie and
// TV titles, for the homepage's quick-search chips.
type Trending struct {
	Movies    []string `json:"movies"` // 欧美 movies, English names
	TV        []string `json:"tv"`     // 美剧, English names
	TVJP      []string `json:"tv_jp"`  // 日剧, Chinese names
	TVKR      []string `json:"tv_kr"`  // 韩剧, Chinese names
	UpdatedAt int64    `json:"updated_at"`
}

// TrendingStatus is the trending fetcher's refresh health, surfaced
// in the admin overview.
type TrendingStatus struct {
	Failing     bool  `json:"failing"`      // latest refresh cycles are failing
	FailStreak  int   `json:"fail_streak"`  // consecutive failed cycles
	LastSuccess int64 `json:"last_success"` // unix time of last fully successful cycle, 0 if none this run
}

// CrawlerStatus describes the crawler for the stats endpoint. The sampler
// fields are the ones to watch when discovery looks slow: Nodes is the pool
// the samplers draw from, and Harvested/Sampled is the average yield per
// answered query.
type CrawlerStatus struct {
	Enabled   bool  `json:"enabled"`
	Seen      int64 `json:"seen_infohashes"`
	Nodes     int   `json:"nodes"`
	Queued    int   `json:"queued_nodes"`
	Sampled   int64 `json:"sampled"`
	SampleErr int64 `json:"sample_errors"`
	Harvested int64 `json:"harvested"`
}

// MetaStatus describes the metadata fetch pipeline's live state, as
// opposed to the cumulative fetch counters in the stats table.
type MetaStatus struct {
	QueueDepth int `json:"queue_depth"`
}

// ScraperStatus describes the tracker-scrape prioritizer. Seeded/Scraped is
// the share of discovered hashes with at least one tracker-known seeder;
// Evicted counts hashes that lost their fetch slot to better-seeded ones.
type ScraperStatus struct {
	Queue      int   `json:"queue"`
	Scraped    int64 `json:"scraped"`
	Seeded     int64 `json:"seeded"`
	ScrapeErrs int64 `json:"scrape_errors"`
	Dropped    int64 `json:"dropped"`
	Evicted    int64 `json:"evicted"`
}

// Server serves the search API.
type Server struct {
	st      *store.Store
	crawler func() CrawlerStatus
	logger  *log.Logger
	mux     *http.ServeMux
	opts    Options

	limiter   *ipLimiter    // nil when rate limiting is disabled
	searchSem chan struct{} // admission gate for search queries

	// Dynamic-limit state, guarded by dynMu. When Options.Settings is
	// wired, syncSettings rebuilds limiter/searchSem/dynSearchTimeout
	// from the registry whenever its version moves; reads go through
	// the helpers below so a rebuild never races a request.
	dynMu            sync.RWMutex
	dynVersion       int64
	dynSearchTimeout time.Duration

	// loginLimiter throttles /api/admin/login attempts per client,
	// independent of (and stricter than) the general API limiter: a
	// guessed-token endpoint must not be brute-forceable at search speed.
	loginLimiter *ipLimiter

	// Cached /api/stats body and empty-query total, both refreshed at most
	// once per StatsTTL. The mutexes double as single-flight: one request
	// pays for the refresh while the rest wait for its result.
	statsMu   sync.Mutex
	statsBody []byte
	statsAt   time.Time
	countMu   sync.Mutex
	countVal  int
	countAt   time.Time
}

// New builds the HTTP handler. crawlerStatus may be nil.
func New(st *store.Store, crawlerStatus func() CrawlerStatus, logger *log.Logger, opts Options) *Server {
	if logger == nil {
		logger = log.Default()
	}
	if opts.MaxInflight <= 0 {
		opts.MaxInflight = defaultMaxInflight
	}
	if opts.SearchTimeout <= 0 {
		opts.SearchTimeout = defaultSearchTimeout
	}
	if opts.StatsTTL <= 0 {
		opts.StatsTTL = defaultStatsTTL
	}
	s := &Server{
		st:           st,
		crawler:      crawlerStatus,
		logger:       logger,
		opts:         opts,
		searchSem:    make(chan struct{}, opts.MaxInflight),
		loginLimiter: newIPLimiter(adminLoginRate, adminLoginBurst),
	}
	if opts.RateRPS > 0 {
		burst := opts.RateBurst
		if burst <= 0 {
			burst = int(opts.RateRPS*10) + 1
		}
		s.limiter = newIPLimiter(opts.RateRPS, burst)
	}
	if opts.Settings != nil {
		// Force the first request (and this constructor) to sync the
		// dynamic limits from the registry.
		s.dynVersion = -1
		s.syncSettings()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/search", s.handleSearch)
	mux.HandleFunc("GET /api/stats", s.handleStats)
	mux.HandleFunc("GET /api/trending", s.handleTrending)
	mux.HandleFunc("GET /api/healthz", s.handleHealth)
	mux.HandleFunc("GET /api/site", s.handleSite)
	s.registerAdminRoutes(mux)
	s.mux = mux
	return s
}

// syncSettings rebuilds the dynamic DoS knobs from the settings
// registry when its version has moved since the last sync. Cheap on
// the no-change path (one version comparison under a read lock).
func (s *Server) syncSettings() {
	if s.opts.Settings == nil {
		return
	}
	v := s.opts.Settings.Version()
	s.dynMu.Lock()
	defer s.dynMu.Unlock()
	if v == s.dynVersion {
		return
	}
	s.dynVersion = v
	mgr := s.opts.Settings
	if rps := mgr.Float(settings.KeyRateLimitRPS); rps > 0 {
		burst := mgr.Int(settings.KeyRateLimitBurst)
		if burst <= 0 {
			burst = int(rps*10) + 1
		}
		s.limiter = newIPLimiter(rps, burst)
	} else {
		s.limiter = nil
	}
	s.searchSem = make(chan struct{}, mgr.Int(settings.KeySearchMaxInflight))
	s.dynSearchTimeout = mgr.Duration(settings.KeySearchTimeout)
}

// rateLimiter returns the current limiter (nil when disabled).
func (s *Server) rateLimiter() *ipLimiter {
	s.dynMu.RLock()
	defer s.dynMu.RUnlock()
	return s.limiter
}

// searchSemaphore returns the current admission-gate channel.
func (s *Server) searchSemaphore() chan struct{} {
	s.dynMu.RLock()
	defer s.dynMu.RUnlock()
	return s.searchSem
}

// searchTimeout returns the per-request database budget: the live
// registry value when settings are wired, else the static option.
func (s *Server) searchTimeout() time.Duration {
	s.dynMu.RLock()
	defer s.dynMu.RUnlock()
	if s.dynSearchTimeout > 0 {
		return s.dynSearchTimeout
	}
	return s.opts.SearchTimeout
}

// rateLimitExempt reports whether a path bypasses the public per-client
// limiter: the health check (monitoring and the deploy script must never
// be throttled behind a flood) and the admin API. Admin paths sit behind
// the admin token — and the login endpoint has its own, stricter
// limiter — so the public limiter buys nothing there; worse, it once let
// an operator lock themselves out by saving an extreme RATE_LIMIT_*
// value, because the settings write that would undo it was itself
// rejected with 429 (and the value persists in the DB across restarts).
func rateLimitExempt(path string) bool {
	return path == "/api/healthz" || path == "/api/admin" ||
		strings.HasPrefix(path, "/api/admin/")
}

// Handler returns the root handler with CORS/OPTIONS support and per-client
// rate limiting for the public API (see rateLimitExempt for the paths that
// bypass it).
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		s.syncSettings()
		if lim := s.rateLimiter(); lim != nil && !rateLimitExempt(r.URL.Path) && !lim.allow(clientKey(r)) {
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		s.mux.ServeHTTP(w, r)
	})
}

type resultItem struct {
	InfoHash  string `json:"info_hash"`
	Name      string `json:"name"`
	Alias     string `json:"alias,omitempty"`
	TotalSize int64  `json:"total_size"`
	FileCount int    `json:"file_count"`
	Files     any    `json:"files"`
	Magnet    string `json:"magnet"`
	CreatedAt int64  `json:"created_at"`
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := truncateUTF8(r.URL.Query().Get("q"), maxQueryLen)
	page := atoiDefault(r.URL.Query().Get("page"), 1)
	if page < 1 {
		page = 1
	}
	pageSize := atoiDefault(r.URL.Query().Get("page_size"), defaultPerPage)
	if pageSize < 1 {
		pageSize = defaultPerPage
	}
	if pageSize > maxPageSize {
		pageSize = maxPageSize
	}
	if (page-1)*pageSize > maxOffset {
		page = maxOffset/pageSize + 1
	}

	// Admission gate: the store serializes queries on one connection, so
	// when all slots are busy piling on more requests only grows the queue.
	// Wait briefly for a slot, then shed load instead of stacking up.
	s.syncSettings()
	sem := s.searchSemaphore()
	select {
	case sem <- struct{}{}:
		defer func() { <-sem }()
	case <-r.Context().Done():
		return
	case <-time.After(admissionWait):
		w.Header().Set("Retry-After", "2")
		writeError(w, http.StatusServiceUnavailable, "server busy")
		return
	}

	// The context bounds database time and dies with the client connection,
	// so queries for hung-up clients stop consuming the SQLite handle.
	ctx, cancel := context.WithTimeout(r.Context(), s.searchTimeout())
	defer cancel()

	var (
		items []store.Torrent
		total int
		err   error
	)
	if strings.TrimSpace(q) == "" {
		// The landing page. Latest walks the created_at index instead of
		// counting matches, and the total comes from the shared cache.
		items, err = s.st.Latest(ctx, page, pageSize)
		if err == nil {
			total, err = s.cachedCount(ctx)
		}
	} else {
		items, total, err = s.st.Search(ctx, q, page, pageSize)
	}
	if err != nil {
		if ctx.Err() != nil {
			w.Header().Set("Retry-After", "5")
			writeError(w, http.StatusServiceUnavailable, "search timed out")
		} else {
			writeError(w, http.StatusInternalServerError, "search failed")
		}
		s.logger.Printf("api: search: %v", err)
		return
	}
	results := make([]resultItem, 0, len(items))
	for _, t := range items {
		results = append(results, resultItem{
			InfoHash:  t.InfoHash,
			Name:      t.Name,
			Alias:     t.Alias,
			TotalSize: t.TotalSize,
			FileCount: t.FileCount,
			Files:     t.Files,
			Magnet:    magnetURI(t.InfoHash, t.Name),
			CreatedAt: t.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"total":     total,
		"page":      page,
		"page_size": pageSize,
		"results":   results,
	})
}

// cachedCount returns the total torrent count, at most StatsTTL stale.
// COUNT(*) walks a full index, so the landing page must not pay for it per
// request. The lock is held across the query on purpose: it makes concurrent
// misses single-flight.
func (s *Server) cachedCount(ctx context.Context) (int, error) {
	s.countMu.Lock()
	defer s.countMu.Unlock()
	if !s.countAt.IsZero() && time.Since(s.countAt) < s.opts.StatsTTL {
		return s.countVal, nil
	}
	n, err := s.st.Count(ctx)
	if err != nil {
		return 0, err
	}
	s.countVal, s.countAt = n, time.Now()
	return n, nil
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	// Single-flight with a TTL: stats aggregate four queries (two of them
	// full scans), so one refresh per TTL serves everyone. The lock being
	// held across the refresh is what keeps a stats flood down to one
	// query stream.
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	if s.statsBody != nil && time.Since(s.statsAt) < s.opts.StatsTTL {
		writeRawJSON(w, http.StatusOK, s.statsBody)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.searchTimeout())
	defer cancel()

	total, err := s.st.Count(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "stats failed")
		return
	}
	counters, err := s.st.Stats(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "stats failed")
		return
	}
	blocked, err := s.st.BlockedCount(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "stats failed")
		return
	}
	pending, err := s.st.PendingReviewCount(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "stats failed")
		return
	}
	resp := map[string]any{
		"torrents":       total,
		"seen":           counters["seen"],
		"fetched":        counters["fetched"],
		"adult_filtered": counters["adult_filtered"],
		"spam_filtered":  counters["spam_filtered"],
		"size_filtered":  counters["size_filtered"],
		// Metadata fetch outcomes. A timeout share near 100% means the
		// fetch pool, not discovery, is what caps indexing throughput.
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
	if s.crawler != nil {
		resp["crawler"] = s.crawler()
	}
	if s.opts.ScraperStatus != nil {
		resp["scraper"] = s.opts.ScraperStatus()
	}
	if s.opts.FeedsStatus != nil {
		resp["feeds"] = s.opts.FeedsStatus()
	}
	body, err := json.Marshal(resp)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "stats failed")
		return
	}
	s.statsBody, s.statsAt = body, time.Now()
	writeRawJSON(w, http.StatusOK, body)
}

func (s *Server) handleTrending(w http.ResponseWriter, r *http.Request) {
	t := Trending{Movies: []string{}, TV: []string{}, TVJP: []string{}, TVKR: []string{}}
	if s.opts.Trending != nil {
		got := s.opts.Trending()
		for dst, src := range map[*[]string][]string{
			&t.Movies: got.Movies,
			&t.TV:     got.TV,
			&t.TVJP:   got.TVJP,
			&t.TVKR:   got.TVKR,
		} {
			if src != nil {
				*dst = src
			}
		}
		t.UpdatedAt = got.UpdatedAt
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func magnetURI(hash, name string) string {
	return "magnet:?xt=urn:btih:" + hash + "&dn=" + url.QueryEscape(name)
}

func atoiDefault(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}

// truncateUTF8 cuts s to at most n bytes without splitting a multi-byte rune.
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeRawJSON(w http.ResponseWriter, code int, body []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	w.Write(body)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
