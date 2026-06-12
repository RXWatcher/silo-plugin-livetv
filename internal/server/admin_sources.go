package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/RXWatcher/silo-plugin-livetv/internal/store"
	"github.com/RXWatcher/silo-plugin-livetv/internal/streamproxy"
)

// auditSourceMutation emits a structured audit-log line for an admin source
// mutation. Credentials are never logged: the URL is run through maskURL and
// headers through maskHeaders (the same redaction used for admin API
// responses), so an audit sink can retain these lines indefinitely without
// holding upstream secrets. The actor is the admin user id reflected onto the
// request context by RequireAdmin.
//
// rawURL/headers are the *pre-mask* values from the request; masking happens
// here so every call site is redacted by construction rather than relying on
// each caller to remember.
func (s *Server) auditSourceMutation(ctx context.Context, action, kind, id, rawURL string, headers map[string]string) {
	args := []any{
		"action", action,
		"source_kind", kind,
		"source_id", id,
		"actor", streamproxy.UserIDFromContext(ctx),
	}
	if rawURL != "" {
		args = append(args, "url", maskURL(rawURL))
	}
	if len(headers) > 0 {
		args = append(args, "http_headers", maskHeaders(headers))
	}
	s.auditLogger().Info("admin source mutation", args...)
}

// maskedValue is the placeholder substituted for any credential we refuse to
// echo back to the admin UI. Non-empty so the operator can see a secret IS set
// without learning its value.
const maskedValue = "REDACTED"

// sensitiveHeaderNames are HTTP header names (lower-cased) whose values carry
// upstream credentials. Their values are masked in admin responses.
var sensitiveHeaderNames = map[string]struct{}{
	"authorization":       {},
	"proxy-authorization": {},
	"cookie":              {},
	"x-api-key":           {},
	"api-key":             {},
	"x-auth-token":        {},
}

// sensitiveQueryKeys are URL query parameters (lower-cased) that carry upstream
// xtream-style credentials. Their values are masked in admin responses.
var sensitiveQueryKeys = map[string]struct{}{
	"username": {},
	"password": {},
	"token":    {},
	"auth":     {},
	"api_key":  {},
	"apikey":   {},
}

// maskHeaders returns a copy of h with the values of credential-bearing headers
// replaced by maskedValue. The original map is never mutated.
func maskHeaders(h map[string]string) map[string]string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		if _, ok := sensitiveHeaderNames[strings.ToLower(k)]; ok && v != "" {
			out[k] = maskedValue
			continue
		}
		out[k] = v
	}
	return out
}

// maskURL redacts credentials embedded in an upstream source URL: any userinfo
// (user:pass@) and any sensitive query parameter value. Unparseable URLs are
// returned verbatim — they hold no parseable secret to leak structurally, and
// the admin still needs to see what was stored to fix it.
func maskURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if u.User != nil {
		if _, hasPass := u.User.Password(); hasPass {
			u.User = url.UserPassword(u.User.Username(), maskedValue)
		} else if u.User.Username() != "" {
			u.User = url.User(maskedValue)
		}
	}
	if q := u.Query(); len(q) > 0 {
		changed := false
		for key, vals := range q {
			if _, ok := sensitiveQueryKeys[strings.ToLower(key)]; !ok {
				continue
			}
			for i := range vals {
				if vals[i] != "" {
					vals[i] = maskedValue
					changed = true
				}
			}
			q[key] = vals
		}
		if changed {
			u.RawQuery = q.Encode()
		}
	}
	return u.String()
}

// adminRefreshTimeout caps the manual refresh background goroutine so a slow
// upstream can't tie up workers indefinitely after the operator returns 202.
const adminRefreshTimeout = 5 * time.Minute

// adminM3USourceDTO is the wire shape for M3U sources on the admin API. We
// surface every column the admin can see (including status metadata) so the
// SPA can render last-refreshed banners without a second round-trip.
type adminM3USourceDTO struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	URL             string            `json:"url"`
	HTTPHeaders     map[string]string `json:"http_headers"`
	Enabled         bool              `json:"enabled"`
	RefreshInterval string            `json:"refresh_interval"`
	LastRefreshedAt *time.Time        `json:"last_refreshed_at,omitempty"`
	LastStatus      string            `json:"last_status,omitempty"`
	ETag            string            `json:"etag,omitempty"`
	LastModified    string            `json:"last_modified,omitempty"`
}

// adminXMLTVSourceDTO mirrors adminM3USourceDTO with the XMLTV-only Gzip flag.
type adminXMLTVSourceDTO struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	URL             string            `json:"url"`
	HTTPHeaders     map[string]string `json:"http_headers"`
	Enabled         bool              `json:"enabled"`
	RefreshInterval string            `json:"refresh_interval"`
	Gzip            bool              `json:"gzip"`
	LastRefreshedAt *time.Time        `json:"last_refreshed_at,omitempty"`
	LastStatus      string            `json:"last_status,omitempty"`
	ETag            string            `json:"etag,omitempty"`
	LastModified    string            `json:"last_modified,omitempty"`
}

// adminM3USourceRequest is the create/update body. RefreshInterval is a Go
// duration string (e.g. "6h", "3h30m") so curl-from-the-shell stays ergonomic.
type adminM3USourceRequest struct {
	Name            string            `json:"name"`
	URL             string            `json:"url"`
	HTTPHeaders     map[string]string `json:"http_headers"`
	Enabled         bool              `json:"enabled"`
	RefreshInterval string            `json:"refresh_interval"`
}

// adminXMLTVSourceRequest mirrors adminM3USourceRequest with the Gzip flag.
type adminXMLTVSourceRequest struct {
	Name            string            `json:"name"`
	URL             string            `json:"url"`
	HTTPHeaders     map[string]string `json:"http_headers"`
	Enabled         bool              `json:"enabled"`
	RefreshInterval string            `json:"refresh_interval"`
	Gzip            bool              `json:"gzip"`
}

// toAdminM3UDTO collapses a store.M3USource onto the wire shape, normalising
// nil header maps to {} so the response always has a stable structure.
func toAdminM3UDTO(src store.M3USource) adminM3USourceDTO {
	return adminM3USourceDTO{
		ID:              src.ID,
		Name:            src.Name,
		URL:             maskURL(src.URL),
		HTTPHeaders:     maskHeaders(src.HTTPHeaders),
		Enabled:         src.Enabled,
		RefreshInterval: src.RefreshInterval.String(),
		LastRefreshedAt: src.LastRefreshedAt,
		LastStatus:      src.LastStatus,
		ETag:            src.ETag,
		LastModified:    src.LastModified,
	}
}

// toAdminXMLTVDTO mirrors toAdminM3UDTO with the Gzip column added.
func toAdminXMLTVDTO(src store.XMLTVSource) adminXMLTVSourceDTO {
	return adminXMLTVSourceDTO{
		ID:              src.ID,
		Name:            src.Name,
		URL:             maskURL(src.URL),
		HTTPHeaders:     maskHeaders(src.HTTPHeaders),
		Enabled:         src.Enabled,
		RefreshInterval: src.RefreshInterval.String(),
		Gzip:            src.Gzip,
		LastRefreshedAt: src.LastRefreshedAt,
		LastStatus:      src.LastStatus,
		ETag:            src.ETag,
		LastModified:    src.LastModified,
	}
}

// parseRefreshInterval converts the user-friendly duration string into a
// time.Duration. Returns an error if the string fails Go's parser; callers
// surface that as a 400.
func parseRefreshInterval(s string) (time.Duration, error) {
	if s == "" {
		return 0, errors.New("refresh_interval is required")
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, err
	}
	if d <= 0 {
		return 0, errors.New("refresh_interval must be positive")
	}
	return d, nil
}

// mountAdminSources registers the source CRUD + refresh routes onto the admin
// chi subrouter. Called from Routes() once the admin group is built.
func (s *Server) mountAdminSources(r chi.Router) {
	r.Route("/sources/m3u", func(m chi.Router) {
		m.Get("/", s.adminListM3USources)
		m.Post("/", s.adminCreateM3USource)
		m.Get("/{id}", s.adminGetM3USource)
		m.Put("/{id}", s.adminUpdateM3USource)
		m.Delete("/{id}", s.adminDeleteM3USource)
		m.Post("/{id}/refresh", s.adminRefreshM3USource)
	})
	r.Route("/sources/xmltv", func(x chi.Router) {
		x.Get("/", s.adminListXMLTVSources)
		x.Post("/", s.adminCreateXMLTVSource)
		x.Get("/{id}", s.adminGetXMLTVSource)
		x.Put("/{id}", s.adminUpdateXMLTVSource)
		x.Delete("/{id}", s.adminDeleteXMLTVSource)
		x.Post("/{id}/refresh", s.adminRefreshXMLTVSource)
	})
}

// adminListM3USources handles GET /admin/sources/m3u.
func (s *Server) adminListM3USources(w http.ResponseWriter, r *http.Request) {
	srcs, err := s.Store.ListM3USources(r.Context())
	if err != nil {
		s.logger().Warn("admin list m3u sources", "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]adminM3USourceDTO, len(srcs))
	for i, src := range srcs {
		out[i] = toAdminM3UDTO(src)
	}
	writeJSON(w, http.StatusOK, listEnvelope[adminM3USourceDTO]{Data: out})
}

// adminCreateM3USource handles POST /admin/sources/m3u.
func (s *Server) adminCreateM3USource(w http.ResponseWriter, r *http.Request) {
	var req adminM3USourceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorMsg(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if req.Name == "" || req.URL == "" {
		writeErrorMsg(w, http.StatusBadRequest, "name and url required")
		return
	}
	d, err := parseRefreshInterval(req.RefreshInterval)
	if err != nil {
		writeErrorMsg(w, http.StatusBadRequest, "invalid refresh_interval: "+err.Error())
		return
	}
	src, err := s.Store.CreateM3USource(r.Context(), store.M3USource{
		Name:            req.Name,
		URL:             req.URL,
		HTTPHeaders:     req.HTTPHeaders,
		Enabled:         req.Enabled,
		RefreshInterval: d,
	})
	if err != nil {
		s.logger().Warn("admin create m3u source", "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.auditSourceMutation(r.Context(), "create", "m3u", src.ID, req.URL, req.HTTPHeaders)
	writeJSON(w, http.StatusCreated, toAdminM3UDTO(src))
}

// adminGetM3USource handles GET /admin/sources/m3u/{id}.
func (s *Server) adminGetM3USource(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	src, err := s.Store.GetM3USource(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErrorMsg(w, http.StatusNotFound, "source not found")
			return
		}
		s.logger().Warn("admin get m3u source", "id", id, "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, toAdminM3UDTO(src))
}

// adminUpdateM3USource handles PUT /admin/sources/m3u/{id}.
func (s *Server) adminUpdateM3USource(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req adminM3USourceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorMsg(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if req.Name == "" || req.URL == "" {
		writeErrorMsg(w, http.StatusBadRequest, "name and url required")
		return
	}
	d, err := parseRefreshInterval(req.RefreshInterval)
	if err != nil {
		writeErrorMsg(w, http.StatusBadRequest, "invalid refresh_interval: "+err.Error())
		return
	}
	if err := s.Store.UpdateM3USource(r.Context(), store.M3USource{
		ID:              id,
		Name:            req.Name,
		URL:             req.URL,
		HTTPHeaders:     req.HTTPHeaders,
		Enabled:         req.Enabled,
		RefreshInterval: d,
	}); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErrorMsg(w, http.StatusNotFound, "source not found")
			return
		}
		s.logger().Warn("admin update m3u source", "id", id, "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.auditSourceMutation(r.Context(), "update", "m3u", id, req.URL, req.HTTPHeaders)
	src, err := s.Store.GetM3USource(r.Context(), id)
	if err != nil {
		s.logger().Warn("admin reload m3u source", "id", id, "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, toAdminM3UDTO(src))
}

// adminDeleteM3USource handles DELETE /admin/sources/m3u/{id}.
func (s *Server) adminDeleteM3USource(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.Store.DeleteM3USource(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErrorMsg(w, http.StatusNotFound, "source not found")
			return
		}
		s.logger().Warn("admin delete m3u source", "id", id, "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.auditSourceMutation(r.Context(), "delete", "m3u", id, "", nil)
	w.WriteHeader(http.StatusNoContent)
}

// adminRefreshM3USource handles POST /admin/sources/m3u/{id}/refresh.
// Refresh runs asynchronously: we fork a goroutine with a fresh context so
// the operator can close the browser without aborting the upstream fetch.
func (s *Server) adminRefreshM3USource(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	// Confirm the source exists before claiming we kicked off a refresh — the
	// 202 should mean "we started something", not "we will silently fail".
	if _, err := s.Store.GetM3USource(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErrorMsg(w, http.StatusNotFound, "source not found")
			return
		}
		s.logger().Warn("admin refresh m3u source preflight", "id", id, "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if s.M3UWorker == nil {
		writeErrorMsg(w, http.StatusServiceUnavailable, "m3u worker not wired")
		return
	}
	logger := s.logger()
	go func(workerID string) {
		ctx, cancel := context.WithTimeout(context.Background(), adminRefreshTimeout)
		defer cancel()
		if err := s.M3UWorker.RefreshOne(ctx, workerID); err != nil {
			logger.Warn("admin manual m3u refresh failed", "id", workerID, "err", err)
		}
	}(id)
	writeJSON(w, http.StatusAccepted, map[string]bool{"started": true})
}

// adminListXMLTVSources handles GET /admin/sources/xmltv.
func (s *Server) adminListXMLTVSources(w http.ResponseWriter, r *http.Request) {
	srcs, err := s.Store.ListXMLTVSources(r.Context())
	if err != nil {
		s.logger().Warn("admin list xmltv sources", "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]adminXMLTVSourceDTO, len(srcs))
	for i, src := range srcs {
		out[i] = toAdminXMLTVDTO(src)
	}
	writeJSON(w, http.StatusOK, listEnvelope[adminXMLTVSourceDTO]{Data: out})
}

// adminCreateXMLTVSource handles POST /admin/sources/xmltv.
func (s *Server) adminCreateXMLTVSource(w http.ResponseWriter, r *http.Request) {
	var req adminXMLTVSourceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorMsg(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if req.Name == "" || req.URL == "" {
		writeErrorMsg(w, http.StatusBadRequest, "name and url required")
		return
	}
	d, err := parseRefreshInterval(req.RefreshInterval)
	if err != nil {
		writeErrorMsg(w, http.StatusBadRequest, "invalid refresh_interval: "+err.Error())
		return
	}
	src, err := s.Store.CreateXMLTVSource(r.Context(), store.XMLTVSource{
		Name:            req.Name,
		URL:             req.URL,
		HTTPHeaders:     req.HTTPHeaders,
		Enabled:         req.Enabled,
		RefreshInterval: d,
		Gzip:            req.Gzip,
	})
	if err != nil {
		s.logger().Warn("admin create xmltv source", "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.auditSourceMutation(r.Context(), "create", "xmltv", src.ID, req.URL, req.HTTPHeaders)
	writeJSON(w, http.StatusCreated, toAdminXMLTVDTO(src))
}

// adminGetXMLTVSource handles GET /admin/sources/xmltv/{id}.
func (s *Server) adminGetXMLTVSource(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	src, err := s.Store.GetXMLTVSource(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErrorMsg(w, http.StatusNotFound, "source not found")
			return
		}
		s.logger().Warn("admin get xmltv source", "id", id, "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, toAdminXMLTVDTO(src))
}

// adminUpdateXMLTVSource handles PUT /admin/sources/xmltv/{id}.
func (s *Server) adminUpdateXMLTVSource(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req adminXMLTVSourceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorMsg(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if req.Name == "" || req.URL == "" {
		writeErrorMsg(w, http.StatusBadRequest, "name and url required")
		return
	}
	d, err := parseRefreshInterval(req.RefreshInterval)
	if err != nil {
		writeErrorMsg(w, http.StatusBadRequest, "invalid refresh_interval: "+err.Error())
		return
	}
	if err := s.Store.UpdateXMLTVSource(r.Context(), store.XMLTVSource{
		ID:              id,
		Name:            req.Name,
		URL:             req.URL,
		HTTPHeaders:     req.HTTPHeaders,
		Enabled:         req.Enabled,
		RefreshInterval: d,
		Gzip:            req.Gzip,
	}); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErrorMsg(w, http.StatusNotFound, "source not found")
			return
		}
		s.logger().Warn("admin update xmltv source", "id", id, "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.auditSourceMutation(r.Context(), "update", "xmltv", id, req.URL, req.HTTPHeaders)
	src, err := s.Store.GetXMLTVSource(r.Context(), id)
	if err != nil {
		s.logger().Warn("admin reload xmltv source", "id", id, "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, toAdminXMLTVDTO(src))
}

// adminDeleteXMLTVSource handles DELETE /admin/sources/xmltv/{id}.
func (s *Server) adminDeleteXMLTVSource(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.Store.DeleteXMLTVSource(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErrorMsg(w, http.StatusNotFound, "source not found")
			return
		}
		s.logger().Warn("admin delete xmltv source", "id", id, "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.auditSourceMutation(r.Context(), "delete", "xmltv", id, "", nil)
	w.WriteHeader(http.StatusNoContent)
}

// adminRefreshXMLTVSource handles POST /admin/sources/xmltv/{id}/refresh.
func (s *Server) adminRefreshXMLTVSource(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := s.Store.GetXMLTVSource(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErrorMsg(w, http.StatusNotFound, "source not found")
			return
		}
		s.logger().Warn("admin refresh xmltv source preflight", "id", id, "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if s.XMLTVWorker == nil {
		writeErrorMsg(w, http.StatusServiceUnavailable, "xmltv worker not wired")
		return
	}
	logger := s.logger()
	go func(workerID string) {
		ctx, cancel := context.WithTimeout(context.Background(), adminRefreshTimeout)
		defer cancel()
		if err := s.XMLTVWorker.RefreshOne(ctx, workerID); err != nil {
			logger.Warn("admin manual xmltv refresh failed", "id", workerID, "err", err)
		}
	}(id)
	writeJSON(w, http.StatusAccepted, map[string]bool{"started": true})
}
