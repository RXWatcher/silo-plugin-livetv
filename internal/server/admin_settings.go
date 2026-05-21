package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ContinuumApp/continuum-plugin-livetv/internal/store"
)

// adminSettingsDTO is the wire shape for /admin/settings. Durations are
// serialised as Go duration strings ("6h", "60s") for shell ergonomics; the
// SPA parses them on read and emits them on write.
type adminSettingsDTO struct {
	DefaultM3URefresh    string `json:"default_m3u_refresh"`
	DefaultXMLTVRefresh  string `json:"default_xmltv_refresh"`
	GuideWindowCap       string `json:"guide_window_cap"`
	PerUserStreamCap     int    `json:"per_user_stream_cap"`
	PerChannelDefaultCap int    `json:"per_channel_default_cap"`
	SessionIdleTimeout   string `json:"session_idle_timeout"`
}

// toAdminSettingsDTO collapses a store.SettingsRow onto the wire shape.
func toAdminSettingsDTO(r store.SettingsRow) adminSettingsDTO {
	return adminSettingsDTO{
		DefaultM3URefresh:    r.DefaultM3URefresh.String(),
		DefaultXMLTVRefresh:  r.DefaultXMLTVRefresh.String(),
		GuideWindowCap:       r.GuideWindowCap.String(),
		PerUserStreamCap:     r.PerUserStreamCap,
		PerChannelDefaultCap: r.PerChannelDefaultCap,
		SessionIdleTimeout:   r.SessionIdleTimeout.String(),
	}
}

// mountAdminSettings registers /admin/settings GET + PUT.
func (s *Server) mountAdminSettings(r chi.Router) {
	r.Get("/settings", s.adminGetSettings)
	r.Put("/settings", s.adminUpdateSettings)
}

// adminGetSettings handles GET /admin/settings.
func (s *Server) adminGetSettings(w http.ResponseWriter, r *http.Request) {
	row, err := s.Store.GetSettings(r.Context())
	if err != nil {
		s.logger().Warn("admin get settings", "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, toAdminSettingsDTO(row))
}

// parseSettingsDuration is a stricter sibling of parseRefreshInterval: it
// surfaces the field name in the error so callers can pin-point which
// duration tripped the validator.
func parseSettingsDuration(field, s string) (time.Duration, error) {
	if s == "" {
		return 0, errors.New(field + " is required")
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, errors.New("invalid " + field + ": " + err.Error())
	}
	if d <= 0 {
		return 0, errors.New(field + " must be positive")
	}
	return d, nil
}

// adminUpdateSettings handles PUT /admin/settings. Validates the payload,
// writes it to the singleton row, then asks the snapshot to reload so the
// stream-proxy and reaper observe the new values without a restart.
func (s *Server) adminUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var req adminSettingsDTO
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorMsg(w, http.StatusBadRequest, "invalid json body")
		return
	}
	dM3U, err := parseSettingsDuration("default_m3u_refresh", req.DefaultM3URefresh)
	if err != nil {
		writeErrorMsg(w, http.StatusBadRequest, err.Error())
		return
	}
	dXMLTV, err := parseSettingsDuration("default_xmltv_refresh", req.DefaultXMLTVRefresh)
	if err != nil {
		writeErrorMsg(w, http.StatusBadRequest, err.Error())
		return
	}
	dGuide, err := parseSettingsDuration("guide_window_cap", req.GuideWindowCap)
	if err != nil {
		writeErrorMsg(w, http.StatusBadRequest, err.Error())
		return
	}
	dIdle, err := parseSettingsDuration("session_idle_timeout", req.SessionIdleTimeout)
	if err != nil {
		writeErrorMsg(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.PerUserStreamCap <= 0 {
		writeErrorMsg(w, http.StatusBadRequest, "per_user_stream_cap must be > 0")
		return
	}
	if req.PerChannelDefaultCap <= 0 {
		writeErrorMsg(w, http.StatusBadRequest, "per_channel_default_cap must be > 0")
		return
	}

	row := store.SettingsRow{
		DefaultM3URefresh:    dM3U,
		DefaultXMLTVRefresh:  dXMLTV,
		GuideWindowCap:       dGuide,
		PerUserStreamCap:     req.PerUserStreamCap,
		PerChannelDefaultCap: req.PerChannelDefaultCap,
		SessionIdleTimeout:   dIdle,
	}
	if err := s.Store.UpdateSettings(r.Context(), row); err != nil {
		s.logger().Warn("admin update settings", "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if s.Snapshot != nil {
		if err := s.Snapshot.Reload(r.Context()); err != nil {
			s.logger().Warn("admin settings snapshot reload", "err", err)
			// We've already persisted; surface the partial failure to the
			// caller so they know the in-memory cache is stale.
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}
	// Re-read so the response reflects whatever the DB normalised.
	current, err := s.Store.GetSettings(r.Context())
	if err != nil {
		s.logger().Warn("admin reload settings", "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, toAdminSettingsDTO(current))
}
