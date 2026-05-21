// Package server hosts the live TV plugin's HTTP API. It composes the
// user-facing REST surface (channels, groups, guide, programs, favorites,
// recent), the stream-proxy byte routes, and the (Phase 7) admin subrouter
// into one chi handler that the httproutes capability bridge serves.
//
// All routes are mounted under BasePath (defaults to /api/v1/livetv) to match
// the convention baked into the stream-proxy cookie path.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/hashicorp/go-hclog"

	"github.com/RXWatcher/continuum-plugin-livetv/internal/store"
	"github.com/RXWatcher/continuum-plugin-livetv/internal/streamproxy"
)

// defaultBasePath mirrors streamproxy.defaultBasePath so the cookie scope and
// the route mount point stay in lockstep.
const defaultBasePath = "/api/v1/livetv"

// RefreshWorker is the minimal surface admin handlers need from a refresh
// worker: re-fetch a single source by id. Both refresh.M3UWorker and
// refresh.XMLTVWorker satisfy this; tests substitute in-memory doubles that
// just record calls.
type RefreshWorker interface {
	RefreshOne(ctx context.Context, id string) error
}

// SettingsReloader is implemented by the DB-backed Snapshot in the settings
// package: PUT /admin/settings calls Reload after persisting so streamproxy
// observers see the new values without restarting the plugin.
type SettingsReloader interface {
	Reload(ctx context.Context) error
}

// Server is the HTTP surface root. Build one in main and serve its Routes()
// from the httproutes capability bridge.
type Server struct {
	Store    *store.Store
	Stream   *streamproxy.Deps
	Settings streamproxy.Settings
	Logger   hclog.Logger
	// BasePath defaults to /api/v1/livetv when empty.
	BasePath string

	// Phase 7 wiring: refresh workers feed /admin/sources/{kind}/{id}/refresh
	// and the settings snapshot reloader is invoked by PUT /admin/settings.
	M3UWorker   RefreshWorker
	XMLTVWorker RefreshWorker
	Snapshot    SettingsReloader
}

// logger returns the configured logger, falling back to a null logger so
// tests don't have to wire one in.
func (s *Server) logger() hclog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return hclog.NewNullLogger()
}

// basePath returns the public mount point, defaulting when blank.
func (s *Server) basePath() string {
	if s.BasePath != "" {
		return s.BasePath
	}
	return defaultBasePath
}

// listEnvelope is the standard wire shape for paginated list responses.
// NextCursor is omitted when empty.
type listEnvelope[T any] struct {
	Data       []T    `json:"data"`
	NextCursor string `json:"next_cursor,omitempty"`
}

// errorEnvelope is the standard wire shape for error responses.
type errorEnvelope struct {
	Error   string `json:"error"`
	Details string `json:"details,omitempty"`
}

// writeJSON encodes v as JSON and writes it with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError encodes err as the standard error envelope. The 4xx/5xx callers
// pick the status; this helper just normalises the JSON shape.
func writeError(w http.ResponseWriter, status int, err error) {
	body := errorEnvelope{Error: http.StatusText(status)}
	if err != nil {
		body.Details = err.Error()
	}
	writeJSON(w, status, body)
}

// writeErrorMsg is a convenience wrapper for callers that want to override the
// canonical message (e.g. "channel not found" instead of "Not Found").
func writeErrorMsg(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorEnvelope{Error: msg})
}

// clampLimit parses a query-string limit param, applies a default when blank
// or non-numeric, and caps it at max.
func clampLimit(raw string, def, max int) int {
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}

// statusForStoreErr maps known store errors onto HTTP status codes. Callers
// pass the result to writeError; unknown errors map to 500.
func statusForStoreErr(err error) int {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return http.StatusNotFound
	default:
		return http.StatusInternalServerError
	}
}
