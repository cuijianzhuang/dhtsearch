package api

import (
	"net/http"
)

// Manual trending refresh (see internal/trending): the fetcher runs
// on its interval with failure backoff, which can leave the console
// staring at a stale snapshot long after upstream recovered. This
// endpoint queues one cycle immediately; the response carries the
// refresh health as it stands at queue time — the new snapshot
// lands seconds later and the console's normal polling picks it up.
//
// Admin role only, like the other state-changing console actions.
// One queued request is enough (it fetches the freshest state), so
// a second trigger while one is still queued conflicts instead of
// stacking duplicate fetches against Douban.

func (s *Server) handleAdminTrendingRefresh(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireRole(w, r, roleAdmin)
	if !ok {
		return
	}
	if s.opts.TrendingRefresh == nil {
		writeError(w, http.StatusServiceUnavailable, "trending refresh unavailable")
		return
	}
	if !s.opts.TrendingRefresh.TriggerRefresh() {
		writeError(w, http.StatusConflict, "已有榜单刷新在排队，请稍候")
		return
	}
	s.logger.Printf("api: trending refresh triggered from admin panel by %s", p.Username)
	resp := map[string]any{"accepted": true}
	if s.opts.TrendingStatus != nil {
		ts := s.opts.TrendingStatus()
		resp["failing"] = ts.Failing
		resp["fail_streak"] = ts.FailStreak
		resp["last_success"] = ts.LastSuccess
	}
	writeJSON(w, http.StatusAccepted, resp)
}
