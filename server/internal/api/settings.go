package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"dhtsearch/server/internal/settings"
)

// Site configuration and the admin settings API.
//
// GET /api/site is public: it carries only the presentation values
// the web app needs at render time (site name, theme color preset,
// default appearance) — the same things a page title would reveal.
// Everything else about the configuration stays behind the admin
// token on /api/admin/settings.
//
// Sensitive values never leave the process through either endpoint:
// the snapshot reports only whether a secret is set, and an update
// that sends the masked placeholder (or an empty string) for a
// secret leaves it unchanged.

// handleSite serves the web app's presentation configuration.
func (s *Server) handleSite(w http.ResponseWriter, r *http.Request) {
	name, color, appearance := "DHTSearch", "teal", "system"
	if s.opts.Settings != nil {
		name = s.opts.Settings.Get(settings.KeySiteName)
		color = s.opts.Settings.Get(settings.KeyThemeColor)
		appearance = s.opts.Settings.Get(settings.KeyThemeAppearance)
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"name":             name,
		"theme_color":      color,
		"theme_appearance": appearance,
	})
}

func (s *Server) handleAdminSettingsGet(w http.ResponseWriter, r *http.Request) {
	// Settings expose the whole deployment configuration (and accept
	// changes to it): admin role only, reviewers never reach here.
	if _, ok := s.requireRole(w, r, roleAdmin); !ok {
		return
	}
	if s.opts.Settings == nil {
		writeError(w, http.StatusServiceUnavailable, "settings unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"groups": s.opts.Settings.Snapshot(),
	})
}

func (s *Server) handleAdminSettingsPut(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireRole(w, r, roleAdmin); !ok {
		return
	}
	if s.opts.Settings == nil {
		writeError(w, http.StatusServiceUnavailable, "settings unavailable")
		return
	}
	// A nil *string deletes the override (revert to env/default).
	var body struct {
		Values map[string]*string `json:"values"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10))
	if err := dec.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad request")
		return
	}
	if len(body.Values) == 0 {
		writeError(w, http.StatusBadRequest, "no values")
		return
	}
	applied, restart, err := s.opts.Settings.Update(body.Values)
	if err != nil {
		var ue *settings.UpdateError
		if errors.As(err, &ue) {
			// Validation is atomic: nothing was saved. The per-key
			// messages let the UI mark exactly which fields are wrong.
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]any{
				"error":  "invalid settings",
				"errors": ue.Errors,
			})
			return
		}
		s.logger.Printf("api: admin settings update: %v", err)
		writeError(w, http.StatusInternalServerError, "settings update failed")
		return
	}
	resp := map[string]any{
		"ok":               true,
		"applied":          applied,
		"restart_required": restart,
	}
	var warnings []string
	// System update: when either AUTO_UPDATE_* key was part of this
	// save, mirror the effective values into the panel contract file
	// the installer script reads (see update.go). The settings
	// themselves are already saved at this point; a sync failure is a
	// warning, not a failed save.
	_, touchedEnabled := body.Values[settings.KeyAutoUpdateEnabled]
	_, touchedTime := body.Values[settings.KeyAutoUpdateTime]
	if touchedEnabled || touchedTime {
		if err := s.syncUpdatePanelFile(); err != nil {
			s.logger.Printf("api: sync update panel file: %v", err)
			warnings = append(warnings, "系统更新设置已保存，但同步给升级脚本失败，自动升级状态可能不生效："+err.Error())
		}
	}
	// Footgun guard: requiring API tokens while the deployment has no
	// INTERNAL_API_TOKEN locks the bundled frontend out along with
	// everyone else (its server-side calls are anonymous without the
	// first-party channel). The save itself is legitimate — an
	// API-only deployment may want exactly that — so warn, don't block.
	if s.opts.InternalToken == "" && s.opts.Settings.Bool(settings.KeyPublicAPIRequireToken) {
		warnings = append(warnings,
			"「公开 API 要求令牌」已开启，但内部令牌（INTERNAL_API_TOKEN）未配置：站点前台自身也会被 401 拦截。请通过 install.sh 升级（会自动生成并同步到 API 与 web 两侧），或在两侧环境变量中设置相同的 INTERNAL_API_TOKEN。")
	}
	if len(warnings) > 0 {
		resp["warnings"] = warnings
	}
	writeJSON(w, http.StatusOK, resp)
}
