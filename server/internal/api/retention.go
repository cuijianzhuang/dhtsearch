package api

import (
	"net/http"
)

// Data-retention surface (see internal/retention): the settings
// page's 数据保留 group carries this card above its form so the
// operator sees the blast radius — how many torrents are eligible
// for deletion under the current settings — before enabling the
// automatic runner, and can fire a one-off pass by hand.
//
// Both endpoints are admin-role only, like the restart actions:
// deletion is irreversible, so reviewers never reach even the
// status. A manual run is an explicit operator action and is NOT
// gated by RETENTION_ENABLED (that switch gates the schedule);
// it honours the configured minimum age like any other pass.

func (s *Server) handleAdminRetentionGet(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireRole(w, r, roleAdmin); !ok {
		return
	}
	if s.opts.Retention == nil {
		writeError(w, http.StatusServiceUnavailable, "retention runner unavailable")
		return
	}
	st, err := s.opts.Retention.Status(r.Context())
	if err != nil {
		s.logger.Printf("api: retention status: %v", err)
		writeError(w, http.StatusInternalServerError, "读取清理状态失败")
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleAdminRetentionRun(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireRole(w, r, roleAdmin)
	if !ok {
		return
	}
	if s.opts.Retention == nil {
		writeError(w, http.StatusServiceUnavailable, "retention runner unavailable")
		return
	}
	if !s.opts.Retention.Trigger() {
		writeError(w, http.StatusConflict, "已有清理正在执行，请等它完成")
		return
	}
	s.logger.Printf("api: retention cleanup triggered from admin panel by %s", p.Username)
	st, err := s.opts.Retention.Status(r.Context())
	if err != nil {
		// The run is already going; a status read failure must not
		// turn the accepted trigger into an error.
		s.logger.Printf("api: retention status after trigger: %v", err)
		writeJSON(w, http.StatusAccepted, map[string]bool{"running": true})
		return
	}
	writeJSON(w, http.StatusAccepted, st)
}
