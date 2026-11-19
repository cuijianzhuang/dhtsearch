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
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":               true,
		"applied":          applied,
		"restart_required": restart,
	})
}
