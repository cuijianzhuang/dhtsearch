package api

import (
	"net/http"
)

// Stall-alert channel probe (see internal/alerts): sends one test
// message to every configured channel and reports each channel's
// outcome in-band, so the operator can verify a webhook URL or a
// Telegram bot before relying on it at 3 a.m. The probe is the
// operator's explicit action, so it runs whether or not
// ALERT_ENABLED is on — that switch gates episode notifications,
// not this test. Admin role only, like the other actions that
// reach outside the process.

func (s *Server) handleAdminAlertsTest(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireRole(w, r, roleAdmin)
	if !ok {
		return
	}
	if s.opts.Alerts == nil {
		writeError(w, http.StatusServiceUnavailable, "alerts service unavailable")
		return
	}
	res := s.opts.Alerts.Test(r.Context())
	if len(res.Channels) == 0 {
		writeError(w, http.StatusBadRequest, "未配置任何告警通道：先在设置页「告警通知」组填写 Webhook 地址或 Telegram 机器人")
		return
	}
	s.logger.Printf("api: alert channel test from admin panel by %s: %v", p.Username, res.Channels)
	writeJSON(w, http.StatusOK, res)
}
