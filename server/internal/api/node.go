package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"dhtsearch/server/internal/filter"
	"dhtsearch/server/internal/metadata"
	"dhtsearch/server/internal/nodepush"
	"dhtsearch/server/internal/settings"
	"dhtsearch/server/internal/store"
)

// Crawler worker node surface (see internal/nodepush for the
// protocol and the worker half). A worker node discovers, scrapes
// and fetches like a standalone node but owns no index: it asks
// this node — the center — which hashes are worth fetching
// (/api/node/check) and pushes what it fetched (/api/node/ingest);
// the center applies its own content filter and stores the records
// as if it had fetched them itself, so policy lives in exactly one
// place no matter how many workers feed it.
//
// The surface has its OWN gate, deliberately separate from both
// the admin console's and the public API's: NODE_INGEST_ENABLED
// (default off) arms it, and the shared NODE_TOKEN secret — read
// live from the settings registry per request, like the public
// gate's knobs — authenticates workers. Both are operator
// settings on the center; a worker configures its side purely by
// environment and never sees this registry.
//
// The endpoints are internal machine-to-machine traffic: they are
// exempt from the public per-IP limiter (like the admin API — the
// token is the credential, and a busy worker legitimately bursts),
// and they are not part of the public API documentation.

const (
	// nodeCheckMaxHashes bounds one admission check call.
	nodeCheckMaxHashes = 500
	// nodeIngestMaxRecords bounds one ingest batch; the pusher
	// builds batches of at most this many.
	nodeIngestMaxRecords = 500
	// nodeIngestMaxSwarm bounds one batch's swarm section.
	nodeIngestMaxSwarm = 5000
	// nodeIngestMaxBody is the ingest request body cap: 500
	// records with full file lists fit comfortably inside it.
	nodeIngestMaxBody = 10 << 20
	// nodeWriteTimeout bounds one record's store work, mirroring
	// the local ingest sink's per-record budget (see onRecord in
	// cmd/server).
	nodeWriteTimeout = 15 * time.Second
)

// registerNodeRoutes wires the node surface into the mux.
func (s *Server) registerNodeRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/node/check", s.handleNodeCheck)
	mux.HandleFunc("POST /api/node/ingest", s.handleNodeIngest)
}

// nodeGate enforces the node surface's own switch and token. It
// returns false when it has already answered: 503 while the
// surface is disabled (or no settings registry is wired), 401 for
// a missing or wrong token. An enabled surface with no token
// configured authenticates nobody — an empty configured secret
// never matches, including an empty presented one.
func (s *Server) nodeGate(w http.ResponseWriter, r *http.Request) bool {
	mgr := s.opts.Settings
	if mgr == nil || !mgr.Bool(settings.KeyNodeIngestEnabled) {
		writeError(w, http.StatusServiceUnavailable, "node ingest disabled")
		return false
	}
	want := mgr.Get(settings.KeyNodeToken)
	got := presentedAPIToken(r)
	if want == "" || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		w.Header().Set("WWW-Authenticate", `Bearer realm="dhtsearch-node"`)
		writeError(w, http.StatusUnauthorized, "invalid node token")
		return false
	}
	return true
}

// handleNodeCheck answers a worker's admission query: which of
// these hashes are already in the center's index, and which are in
// an unexpired failure backoff. The lookups are the same ones the
// local admission gate makes (store.FetchAdmission), one per hash.
func (s *Server) handleNodeCheck(w http.ResponseWriter, r *http.Request) {
	if !s.nodeGate(w, r) {
		return
	}
	var body nodepush.CheckRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad request")
		return
	}
	if len(body.Hashes) > nodeCheckMaxHashes {
		writeError(w, http.StatusRequestEntityTooLarge, "too many hashes")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), nodeWriteTimeout)
	defer cancel()
	resp := nodepush.CheckResponse{Stored: []string{}, Backoff: []string{}}
	now := time.Now().Unix()
	for _, h := range body.Hashes {
		h = strings.ToLower(strings.TrimSpace(h))
		if !validInfoHash(h) {
			continue
		}
		stored, nextRetryAt, err := s.st.FetchAdmission(ctx, h)
		if err != nil {
			s.logger.Printf("api: node check %s: %v", h, err)
			writeError(w, http.StatusInternalServerError, "check failed")
			return
		}
		switch {
		case stored:
			resp.Stored = append(resp.Stored, h)
		case nextRetryAt > now:
			resp.Backoff = append(resp.Backoff, h)
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleNodeIngest accepts one worker batch: records go through
// this node's content filter and into its store exactly as a local
// fetch would (same drop counters, same upsert, same negative-cache
// clearing on success), and the batch's swarm observations merge
// through the same store write the local scrape sink uses.
func (s *Server) handleNodeIngest(w http.ResponseWriter, r *http.Request) {
	if !s.nodeGate(w, r) {
		return
	}
	var body nodepush.IngestRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, nodeIngestMaxBody))
	if err := dec.Decode(&body); err != nil {
		if strings.Contains(err.Error(), "request body too large") {
			writeError(w, http.StatusRequestEntityTooLarge, "batch too large")
			return
		}
		writeError(w, http.StatusBadRequest, "bad request")
		return
	}
	if len(body.Records) > nodeIngestMaxRecords || len(body.Swarm) > nodeIngestMaxSwarm {
		writeError(w, http.StatusRequestEntityTooLarge, "batch too large")
		return
	}
	nodeKey := nodeIngestStatKey(body.Node)
	resp := nodepush.IngestResponse{}
	now := time.Now().Unix()

	for _, wire := range body.Records {
		hash := strings.ToLower(strings.TrimSpace(wire.InfoHash))
		if !validInfoHash(hash) {
			continue
		}
		rec := wire.Record()
		rec.InfoHash = hash
		srcKey := metadata.SourceStatKey(rec.Source, metadata.SourceCounterAttempts)
		fetchedKey := metadata.SourceStatKey(rec.Source, metadata.SourceCounterFetched)

		wctx, wcancel := context.WithTimeout(r.Context(), nodeWriteTimeout)
		// The fetch happened (on the worker): the per-source
		// attempt and fetched counters move for every record
		// received, mirroring the local pipeline, where the
		// fetcher counts a record before the filter sees it.
		s.st.IncrStatContext(wctx, srcKey, 1)
		s.st.IncrStatContext(wctx, fetchedKey, 1)

		res := filter.Check(rec.Name, rec.Files, rec.TotalSize)
		switch {
		case res.Adult:
			s.st.IncrStatContext(wctx, "adult_filtered", 1)
			resp.Filtered++
			wcancel()
			continue
		case res.Blocked:
			s.st.IncrStatContext(wctx, "blocked_filtered", 1)
			resp.Filtered++
			wcancel()
			continue
		case res.Spam:
			s.st.IncrStatContext(wctx, "spam_filtered", 1)
			resp.Filtered++
			wcancel()
			continue
		case res.TooSmall:
			s.st.IncrStatContext(wctx, "size_filtered", 1)
			resp.Filtered++
			wcancel()
			continue
		}
		if err := s.st.UpsertContext(wctx, store.Torrent{
			InfoHash:  rec.InfoHash,
			Name:      rec.Name,
			TotalSize: rec.TotalSize,
			FileCount: rec.FileCount,
			Files:     rec.Files,
			CreatedAt: now,
			Alias:     wire.Alias,
		}); err != nil {
			s.logger.Printf("api: node ingest upsert %s from %q: %v", rec.InfoHash, body.Node, err)
			wcancel()
			continue
		}
		// A stored record retires the hash's failure state, the
		// same clearing the local gate performs on a fetch success.
		if err := s.st.ClearFetchAttempt(wctx, rec.InfoHash); err != nil {
			s.logger.Printf("api: node ingest clear attempt %s: %v", rec.InfoHash, err)
		}
		s.st.IncrStatContext(wctx, "fetched", 1)
		s.st.IncrStatContext(wctx, nodeKey, 1)
		resp.Ingested++
		wcancel()
	}

	if len(body.Swarm) > 0 {
		rows := make([]store.SwarmStats, 0, len(body.Swarm))
		for _, sw := range body.Swarm {
			hash := strings.ToLower(strings.TrimSpace(sw.InfoHash))
			if !validInfoHash(hash) {
				continue
			}
			rows = append(rows, store.SwarmStats{
				InfoHash:  hash,
				Seeders:   sw.Seeders,
				Leechers:  sw.Leechers,
				Completed: sw.Completed,
				ScrapedAt: now,
			})
		}
		if len(rows) > 0 {
			ctx, cancel := context.WithTimeout(r.Context(), nodeWriteTimeout)
			if err := s.st.UpsertSwarmStats(ctx, rows); err != nil {
				s.logger.Printf("api: node ingest swarm from %q: %v", body.Node, err)
			} else {
				resp.Swarm = len(rows)
			}
			cancel()
		}
	}
	if resp.Ingested > 0 || resp.Filtered > 0 {
		s.logger.Printf("api: node ingest from %q: %d stored, %d filtered, %d swarm", body.Node, resp.Ingested, resp.Filtered, resp.Swarm)
	}
	writeJSON(w, http.StatusOK, resp)
}

// nodeIngestStatKey maps a worker-reported node name to its
// per-node ingest counter key (node_ingest_<name>). Names are
// operator-chosen (default: the worker's hostname), so the key is
// built from a sanitized form: lowercase alphanumerics, dash and
// underscore survive, everything else folds to underscore, capped
// at 32 characters; an empty name counts as "unknown".
func nodeIngestStatKey(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
		if b.Len() >= 32 {
			break
		}
	}
	if b.Len() == 0 {
		return "node_ingest_unknown"
	}
	return "node_ingest_" + b.String()
}
