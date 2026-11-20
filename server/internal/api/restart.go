package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Panel-requested service restart: making restart-required settings
// take effect without an SSH session.
//
// The channel is the update flow's (see update.go): the API process
// is unprivileged and never restarts anything itself. It writes an
// intent file into the data dir, and the root-side installer agent
// (scripts/install.sh, on its per-minute timer) consumes it:
//
//	restart-request  panel -> script: a restart request flag
//	                 (REQUEST_ID/REQUESTED_AT/REQUESTED_BY),
//	                 consumed exactly once and deleted.
//	restart-status   script -> panel: the agent's record of the
//	                 last requested restart (STATE/REQUEST_ID/
//	                 timestamps/MESSAGE).
//
// The agent restarts only dhtsearch.service — the API/crawler
// process, which is where restart-required settings live. The web
// service keeps serving the admin UI throughout, so the panel stays
// up to watch the API go away and come back.

const (
	restartRequestFileName = "restart-request"
	restartStatusFileName  = "restart-status"
)

// restartRequestFreshness bounds how long an unconsumed request
// blocks a new one. The agent ticks every minute, so a request
// older than this is never going to be consumed (an installer too
// old to know the restart contract, a stopped timer): the panel may
// then write a fresh one instead of being told "pending" forever.
const restartRequestFreshness = 2 * time.Minute

// restartState is the panel-visible state of the last requested
// restart, merged from the request flag and the agent's status file.
type restartState struct {
	State       string `json:"state"` // idle | requested | running | succeeded | failed
	Pending     bool   `json:"pending"`
	RequestID   string `json:"request_id"`
	RequestedAt string `json:"requested_at"`
	RequestedBy string `json:"requested_by"`
	StartedAt   string `json:"started_at"`
	FinishedAt  string `json:"finished_at"`
	Message     string `json:"message"`
	// ContractReady mirrors the update payload's field of the same
	// name: false on deployments whose data dir the API cannot
	// reach, where the intent-file channel does not exist.
	ContractReady bool `json:"contract_ready"`
}

func (s *Server) restartState() restartState {
	out := restartState{State: "idle", ContractReady: s.opts.DataDir != ""}
	if s.opts.DataDir == "" {
		return out
	}
	status, _ := parseKVFile(filepath.Join(s.opts.DataDir, restartStatusFileName))
	if status != nil {
		out.State = status["STATE"]
		if out.State == "" {
			out.State = "idle"
		}
		out.RequestID = status["REQUEST_ID"]
		out.RequestedAt = status["REQUESTED_AT"]
		out.RequestedBy = status["REQUESTED_BY"]
		out.StartedAt = status["STARTED_AT"]
		out.FinishedAt = status["FINISHED_AT"]
		out.Message = status["MESSAGE"]
	}
	if req, ok := parseKVFile(filepath.Join(s.opts.DataDir, restartRequestFileName)); ok {
		out.Pending = true
		// A request the agent has not picked up yet (its status
		// file does not carry this request id) reads as "requested"
		// even when an older run's status is still on disk.
		if out.RequestID != req["REQUEST_ID"] && out.State != "running" {
			out.State = "requested"
			out.RequestID = req["REQUEST_ID"]
			out.RequestedAt = req["REQUESTED_AT"]
			out.RequestedBy = req["REQUESTED_BY"]
			out.StartedAt, out.FinishedAt = "", ""
			out.Message = ""
		}
	}
	if out.State == "running" {
		out.Pending = true
	}
	return out
}

// restartRequestStale reports whether the pending request flag has
// sat unconsumed past restartRequestFreshness (see the constant).
// An unreadable flag counts as fresh: when in doubt, the request
// that was written first wins.
func (s *Server) restartRequestStale() bool {
	fi, err := os.Stat(filepath.Join(s.opts.DataDir, restartRequestFileName))
	if err != nil {
		return false
	}
	return time.Since(fi.ModTime()) > restartRequestFreshness
}

// --- handlers (admin role only, same as the update actions: a
// restart steers the host's service, so reviewers never reach it) ---

func (s *Server) handleAdminRestartGet(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireRole(w, r, roleAdmin); !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.restartState())
}

func (s *Server) handleAdminRestartRequest(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireRole(w, r, roleAdmin)
	if !ok {
		return
	}
	if s.opts.DataDir == "" {
		writeError(w, http.StatusServiceUnavailable, "restart contract unavailable (no data dir)")
		return
	}
	state := s.restartState()
	if state.State == "running" {
		writeError(w, http.StatusConflict, "服务正在重启中，请等它恢复")
		return
	}
	if state.Pending && !s.restartRequestStale() {
		writeError(w, http.StatusConflict, "已有重启请求在等待执行，请等它完成")
		return
	}
	id := strconv.FormatInt(time.Now().UnixNano(), 10)
	err := writeKVFileAtomic(s.opts.DataDir, restartRequestFileName, [][2]string{
		{"REQUEST_ID", id},
		{"REQUESTED_AT", time.Now().Format("2006-01-02 15:04:05")},
		{"REQUESTED_BY", p.Username},
	})
	if err != nil {
		s.logger.Printf("api: write restart request: %v", err)
		writeError(w, http.StatusInternalServerError, "写入重启请求失败")
		return
	}
	s.logger.Printf("api: service restart requested from admin panel by %s (request %s)", p.Username, id)
	writeJSON(w, http.StatusAccepted, s.restartState())
}
