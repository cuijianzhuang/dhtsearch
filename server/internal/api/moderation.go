package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"dhtsearch/server/internal/moderator"
	"dhtsearch/server/internal/settings"
)

// Admin LLM-moderation endpoints backing the console's /admin/llm
// page. The moderation *configuration* itself stays in the settings
// registry (edited through PUT /api/admin/settings like everything
// else); these two routes serve what settings cannot: the live queue
// state, the audit log, and a connection probe.

// modTestCooldown is the minimum spacing between two connection
// tests, enforced server-side so a stuck button (or a script) cannot
// turn the probe into a paid-request generator.
const modTestCooldown = 5 * time.Second

// handleAdminModeration serves the LLM page's state: whether the
// pass is on, the queue backlog, the cumulative counters and the
// newest audit-log records.
func (s *Server) handleAdminModeration(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if s.opts.Settings == nil {
		writeError(w, http.StatusServiceUnavailable, "settings unavailable")
		return
	}
	mgr := s.opts.Settings
	ctx, cancel := s.adminCtx(r)
	defer cancel()

	pending, err := s.st.PendingReviewCount(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "moderation failed")
		return
	}
	counters, err := s.st.Stats(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "moderation failed")
		return
	}
	recent, err := s.st.RecentModeration(ctx, 50)
	if err != nil {
		s.logger.Printf("api: admin moderation recent: %v", err)
		writeError(w, http.StatusInternalServerError, "moderation failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":  mgr.Bool(settings.KeyModerationEnabled),
		"key_set":  mgr.Get(settings.KeyOpenAIAPIKey) != "",
		"model":    mgr.Get(settings.KeyOpenAIModel),
		"base_url": mgr.Get(settings.KeyOpenAIBaseURL),
		"dry_run":  mgr.Bool(settings.KeyModerationDryRun),
		"pending":  pending,
		"stats": map[string]any{
			"reviewed":       counters["llm_reviewed"],
			"adult_removed":  counters["llm_adult_removed"],
			"spam_removed":   counters["llm_spam_removed"],
			"errors":         counters["llm_errors"],
			"titles_trimmed": counters["llm_titles_trimmed"],
		},
		"recent": recent,
	})
}

// handleAdminModerationTest runs one minimal chat completion against
// the configured (or request-supplied) endpoint and reports whether
// it worked. The optional body lets the page test edited values
// before saving them; an empty field falls back to the effective
// setting, and an empty api_key falls back to the stored key (which
// is never echoed back). The result is reported in-band with a 200 —
// a failed probe is an answer, not a server error — except for the
// debounce, which answers 429.
func (s *Server) handleAdminModerationTest(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if s.opts.Settings == nil {
		writeError(w, http.StatusServiceUnavailable, "settings unavailable")
		return
	}
	// Debounce: one probe in flight at a time, and a short cooldown
	// after each completed one.
	s.modTestMu.Lock()
	if s.modTestBusy || time.Since(s.modTestLast) < modTestCooldown {
		s.modTestMu.Unlock()
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusTooManyRequests, "connection test already ran just now; wait a few seconds")
		return
	}
	s.modTestBusy = true
	s.modTestMu.Unlock()
	defer func() {
		s.modTestMu.Lock()
		s.modTestBusy = false
		s.modTestLast = time.Now()
		s.modTestMu.Unlock()
	}()

	var body struct {
		BaseURL string `json:"base_url"`
		Model   string `json:"model"`
		APIKey  string `json:"api_key"`
	}
	if r.Body != nil {
		// Body is optional: a malformed one is ignored in favour of
		// the saved settings rather than failing the probe.
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&body)
	}
	mgr := s.opts.Settings
	baseURL := body.BaseURL
	if baseURL == "" {
		baseURL = mgr.Get(settings.KeyOpenAIBaseURL)
	}
	model := body.Model
	if model == "" {
		model = mgr.Get(settings.KeyOpenAIModel)
	}
	apiKey := body.APIKey
	if apiKey == "" {
		// The stored key (settings.KeyOpenAIAPIKey) — read it here
		// only to hand to the probe; it is never written to a response.
		apiKey = mgr.Get("OPENAI_API_KEY")
	}

	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	latency, err := moderator.TestConnection(ctx, moderator.Config{
		BaseURL: baseURL,
		APIKey:  apiKey,
		Model:   model,
		Timeout: 30 * time.Second,
	})
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":    false,
			"model": model,
			"error": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"model":      model,
		"latency_ms": latency.Milliseconds(),
	})
}
