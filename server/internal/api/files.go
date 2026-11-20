package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"dhtsearch/server/internal/filter"
)

// File-list pagination shared by the public and admin detail views.
//
// A torrent's files used to travel whole: the admin detail endpoint
// embedded the entire stored list and the public search embedded it in
// every result item. A 5000-file torrent produced a ~300 KB response
// and a ~15k-node DOM on the admin detail page. Both detail surfaces
// now page the list through the endpoints below (100 per page by
// default, 500 max), and the search response embeds only the first
// page as a preview.
const (
	defaultFilesPageSize = 100
	maxFilesPageSize     = 500
	// maxEmbeddedFiles caps how many file entries a search result item
	// carries — exactly one files page, so the expanded card can render
	// its first page without a second request.
	maxEmbeddedFiles = defaultFilesPageSize
)

// filesPageResponse is the wire shape of both files endpoints.
type filesPageResponse struct {
	InfoHash   string        `json:"info_hash"`
	Total      int           `json:"total"`
	Page       int           `json:"page"`
	PageSize   int           `json:"page_size"`
	TotalPages int           `json:"total_pages"`
	Files      []filter.File `json:"files"`
}

// parseFilesPaging reads ?page=&page_size= with the files defaults:
// page clamps up to 1, page_size falls back to the default when absent
// or unparsable and is capped at maxFilesPageSize.
func parseFilesPaging(r *http.Request) (page, pageSize int) {
	page, pageSize = 1, defaultFilesPageSize
	q := r.URL.Query()
	if v, err := strconv.Atoi(q.Get("page")); err == nil && v >= 1 {
		page = v
	}
	if v, err := strconv.Atoi(q.Get("page_size")); err == nil && v >= 1 {
		pageSize = v
	}
	if pageSize > maxFilesPageSize {
		pageSize = maxFilesPageSize
	}
	return page, pageSize
}

// serveFilesPage is the shared tail of both files handlers: one store
// page, 404 for an unknown hash, the uniform response shape.
func (s *Server) serveFilesPage(w http.ResponseWriter, ctx context.Context, hash string, page, pageSize int, what string) {
	files, total, found, err := s.st.FilesPage(ctx, hash, page, pageSize)
	if err != nil {
		s.searchError(w, ctx, err, what)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	totalPages := (total + pageSize - 1) / pageSize
	if totalPages < 1 {
		totalPages = 1
	}
	writeJSON(w, http.StatusOK, filesPageResponse{
		InfoHash:   hash,
		Total:      total,
		Page:       page,
		PageSize:   pageSize,
		TotalPages: totalPages,
		Files:      files,
	})
}

// handleTorrentFiles serves GET /api/torrents/{hash}/files: one page of
// a torrent's file list for the public detail (expanded search card).
// Like the similar lookup it is admission-gated but not token-gated —
// it exposes nothing the search response did not already embed.
func (s *Server) handleTorrentFiles(w http.ResponseWriter, r *http.Request) {
	release, ok := s.gate(w, r)
	if !ok {
		return
	}
	defer release()
	ctx, cancel := context.WithTimeout(r.Context(), s.searchTimeout())
	defer cancel()
	page, pageSize := parseFilesPaging(r)
	s.serveFilesPage(w, ctx, strings.ToLower(r.PathValue("hash")), page, pageSize, "files")
}

// handleAdminTorrentFiles serves GET /api/admin/torrents/{hash}/files:
// the admin detail page's file list, one page at a time.
func (s *Server) handleAdminTorrentFiles(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	ctx, cancel := s.adminCtx(r)
	defer cancel()
	page, pageSize := parseFilesPaging(r)
	s.serveFilesPage(w, ctx, strings.ToLower(r.PathValue("hash")), page, pageSize, "admin files")
}
