package server

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/RXWatcher/continuum-plugin-livetv/internal/store"
)

// adminSessionDTO is the wire shape for /admin/sessions list entries. We
// surface the streaming bookkeeping the operator cares about (bytes, last
// activity stamp) plus the requesting user agent / IP for incident triage.
type adminSessionDTO struct {
	ID            string    `json:"id"`
	UserID        string    `json:"user_id"`
	ChannelID     string    `json:"channel_id"`
	StartedAt     time.Time `json:"started_at"`
	LastByteAt    time.Time `json:"last_byte_at"`
	BytesStreamed int64     `json:"bytes_streamed"`
	ClientIP      string    `json:"client_ip,omitempty"`
	UserAgent     string    `json:"user_agent,omitempty"`
}

// toAdminSessionDTO collapses a store.Session onto the wire shape.
func toAdminSessionDTO(s store.Session) adminSessionDTO {
	return adminSessionDTO{
		ID:            s.ID,
		UserID:        s.UserID,
		ChannelID:     s.ChannelID,
		StartedAt:     s.StartedAt,
		LastByteAt:    s.LastByteAt,
		BytesStreamed: s.BytesStreamed,
		ClientIP:      s.ClientIP,
		UserAgent:     s.UserAgent,
	}
}

// mountAdminSessions registers the /admin/sessions routes.
func (s *Server) mountAdminSessions(r chi.Router) {
	r.Get("/sessions", s.adminListSessions)
	r.Post("/sessions/{id}/kill", s.adminKillSession)
}

// adminListSessions handles GET /admin/sessions — currently active rows only.
func (s *Server) adminListSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := s.Store.ListActiveSessions(r.Context())
	if err != nil {
		s.logger().Warn("admin list sessions", "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]adminSessionDTO, len(sessions))
	for i, sess := range sessions {
		out[i] = toAdminSessionDTO(sess)
	}
	writeJSON(w, http.StatusOK, listEnvelope[adminSessionDTO]{Data: out})
}

// adminKillSession handles POST /admin/sessions/{id}/kill. Idempotent: the
// store's EndSession is a no-op for already-ended sessions, so we return 204
// regardless. (Unknown ids also hit the no-op path; admin tooling shouldn't
// be flooded with 404s for stale UI state.)
func (s *Server) adminKillSession(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.Store.EndSession(r.Context(), id, "admin_kill"); err != nil {
		s.logger().Warn("admin kill session", "id", id, "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
