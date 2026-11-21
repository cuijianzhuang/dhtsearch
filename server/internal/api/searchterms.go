package api

import (
	"context"
	"net/http"
	"time"

	"dhtsearch/server/internal/store"
)

// This file serves GET /api/trending-terms: the site's own hot
// searches, counted anonymously by internal/searchstats (see
// store.NormalizeSearchTerm for what a "term" is). It is a public
// endpoint and sits behind the same PUBLIC_API_* gate as its
// siblings (/api/search, /api/stats, /api/trending).

const (
	defaultTrendingTermsLimit = 20
	maxTrendingTermsLimit     = 50
)

// handleTrendingTerms answers the top search terms for a window:
// "today" (the current UTC day bucket) or "7d" (today and the six
// days before it). The counts ride along for API consumers; the
// homepage chips show the terms only.
func (s *Server) handleTrendingTerms(w http.ResponseWriter, r *http.Request) {
	window := r.URL.Query().Get("window")
	days := 1
	switch window {
	case "", "today":
		window = "today"
	case "7d":
		days = 7
	default:
		writeError(w, http.StatusBadRequest, "invalid window")
		return
	}
	limit := atoiDefault(r.URL.Query().Get("limit"), defaultTrendingTermsLimit)
	if limit < 1 {
		limit = defaultTrendingTermsLimit
	}
	if limit > maxTrendingTermsLimit {
		limit = maxTrendingTermsLimit
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.searchTimeout())
	defer cancel()
	sinceDay := store.SearchTermDay(time.Now()) - int64(days) + 1
	terms, err := s.st.TopSearchTerms(ctx, sinceDay, limit)
	if err != nil {
		s.logger.Printf("api: trending terms: %v", err)
		writeError(w, http.StatusInternalServerError, "trending terms failed")
		return
	}
	if terms == nil {
		terms = []store.SearchTermStat{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"window": window,
		"terms":  terms,
	})
}
