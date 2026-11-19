package api

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"
	"time"

	"dhtsearch/server/internal/store"
)

// Public front-facing extras beyond the core search: the keyword RSS
// feed, the visitor report intake and the similar-torrents lookup that
// backs the search page's expanded detail view.

// --- RSS ---

const rssMaxItems = 50

type rssDocument struct {
	XMLName xml.Name   `xml:"rss"`
	Version string     `xml:"version,attr"`
	Channel rssChannel `xml:"channel"`
}

type rssChannel struct {
	Title       string    `xml:"title"`
	Link        string    `xml:"link"`
	Description string    `xml:"description"`
	Items       []rssItem `xml:"item"`
}

type rssItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	GUID        string `xml:"guid"`
	PubDate     string `xml:"pubDate"`
	Description string `xml:"description"`
	Category    string `xml:"category,omitempty"`
}

// handleRSS serves GET /rss?q=&category= as an RSS 2.0 feed of the
// newest matches, so a keyword search can be subscribed in a reader.
// Without q it is the firehose of latest additions. Item links are the
// magnet URIs themselves — the standard shape for torrent feeds.
func (s *Server) handleRSS(w http.ResponseWriter, r *http.Request) {
	q := truncateUTF8(r.URL.Query().Get("q"), maxQueryLen)
	category := r.URL.Query().Get("category")
	if category != "" && !store.ValidCategory(category) {
		writeError(w, http.StatusBadRequest, "invalid category")
		return
	}
	release, ok := s.gate(w, r)
	if !ok {
		return
	}
	defer release()
	ctx, cancel := context.WithTimeout(r.Context(), s.searchTimeout())
	defer cancel()

	items, _, _, err := s.st.SearchEx(ctx, store.SearchParams{
		Query:    q,
		Category: category,
		Sort:     store.SortNewest,
		Page:     1,
		PageSize: rssMaxItems,
	})
	if err != nil {
		s.searchError(w, ctx, err, "rss")
		return
	}
	scheme := "http"
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = proto
	} else if r.TLS != nil {
		scheme = "https"
	}
	base := scheme + "://" + r.Host
	title := "DHTSearch 最新收录"
	if strings.TrimSpace(q) != "" {
		title = "DHTSearch 搜索：" + q
	}
	trackers := s.trackers()
	doc := rssDocument{
		Version: "2.0",
		Channel: rssChannel{
			Title:       title,
			Link:        base + "/",
			Description: "基于 DHT 网络实时收录的磁力链接，已过滤成人内容与垃圾信息。",
		},
	}
	for _, t := range items {
		doc.Channel.Items = append(doc.Channel.Items, rssItem{
			Title:   t.Name,
			Link:    magnetURI(t.InfoHash, t.Name, trackers),
			GUID:    t.InfoHash,
			PubDate: time.Unix(t.CreatedAt, 0).UTC().Format(time.RFC1123Z),
			Description: fmt.Sprintf("大小 %.2f MB · %d 个文件",
				float64(t.TotalSize)/(1<<20), t.FileCount),
			Category: t.Category,
		})
	}
	out, err := xml.Marshal(doc)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "feed failed")
		return
	}
	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(xml.Header))
	w.Write(out)
}

// --- Similar torrents ---

// handleSimilar serves GET /api/torrents/{hash}/similar: a handful of
// recommendations for the search page's detail view, computed by the
// store from the torrent's own name tokens.
func (s *Server) handleSimilar(w http.ResponseWriter, r *http.Request) {
	release, ok := s.gate(w, r)
	if !ok {
		return
	}
	defer release()
	ctx, cancel := context.WithTimeout(r.Context(), s.searchTimeout())
	defer cancel()

	hash := strings.ToLower(r.PathValue("hash"))
	target, err := s.st.Get(ctx, hash)
	if err != nil {
		s.searchError(w, ctx, err, "similar")
		return
	}
	if target == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	limit := atoiDefault(r.URL.Query().Get("limit"), 6)
	if limit < 1 {
		limit = 6
	}
	if limit > 12 {
		limit = 12
	}
	similar, err := s.st.Similar(ctx, hash, limit)
	if err != nil {
		s.searchError(w, ctx, err, "similar")
		return
	}
	trackers := s.trackers()
	results := make([]resultItem, 0, len(similar))
	for _, t := range similar {
		item := s.toResultItem(t, trackers)
		item.Files = nil // the recommendation row needs no file list
		results = append(results, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

// --- Visitor reports ---

// Report reasons: the preset keys the report form offers. Anything
// else is rejected — free-text reasons would make the queue
// unparseable; the nuance belongs in the note.
var reportReasons = map[string]bool{
	"adult":        true, // 成人内容
	"spam":         true, // 垃圾信息
	"infringement": true, // 侵权
	"other":        true, // 其他
}

const (
	reportRate  = 0.05 // sustained reports per minute-scale: 3/min per client+torrent
	reportBurst = 3
	maxNoteLen  = 500
)

// handleReport serves POST /api/report: a visitor flags an indexed
// torrent for the operator. The per-client+torrent limiter keeps one
// visitor from flooding the queue with duplicates of one complaint;
// the general API limiter still applies on top.
func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
	var body struct {
		InfoHash string `json:"info_hash"`
		Reason   string `json:"reason"`
		Note     string `json:"note"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	if err := dec.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad request")
		return
	}
	hash := strings.ToLower(strings.TrimSpace(body.InfoHash))
	if !validInfoHash(hash) {
		writeError(w, http.StatusBadRequest, "invalid info_hash")
		return
	}
	if !reportReasons[body.Reason] {
		writeError(w, http.StatusBadRequest, "invalid reason")
		return
	}
	note := truncateUTF8(strings.TrimSpace(body.Note), maxNoteLen)

	client := clientKey(r)
	if !s.reportLimiter.allow(client + "|" + hash) {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "too many reports for this torrent")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.searchTimeout())
	defer cancel()
	target, err := s.st.Get(ctx, hash)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "report failed")
		return
	}
	if target == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err := s.st.AddReport(ctx, hash, body.Reason, note, client, time.Now().Unix()); err != nil {
		s.logger.Printf("api: report: %v", err)
		writeError(w, http.StatusInternalServerError, "report failed")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]bool{"ok": true})
}

// validInfoHash reports whether s is a 40-char hex info hash.
func validInfoHash(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
