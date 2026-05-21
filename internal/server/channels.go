package server

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/RXWatcher/continuum-plugin-livetv/internal/store"
	"github.com/RXWatcher/continuum-plugin-livetv/internal/streamproxy"
)

// listChannels handles GET /channels. Query params:
//   - group:  exact match on effective group title
//   - q:      case-insensitive substring on display_name
//   - limit:  1..500, default 100
//   - cursor: opaque pagination cursor from a prior response
func (s *Server) listChannels(w http.ResponseWriter, r *http.Request) {
	userID := streamproxy.UserIDFromContext(r.Context())
	q := r.URL.Query()
	limit := clampLimit(q.Get("limit"), 100, 500)

	channels, next, err := s.Store.ListChannelsForUser(
		r.Context(), userID,
		q.Get("group"), q.Get("q"),
		limit, q.Get("cursor"),
	)
	if err != nil {
		s.logger().Warn("list channels failed", "user_id", userID, "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	dtos := make([]channelDTO, len(channels))
	for i, c := range channels {
		dtos[i] = toChannelDTO(c)
	}
	writeJSON(w, http.StatusOK, listEnvelope[channelDTO]{Data: dtos, NextCursor: next})
}

// getChannel handles GET /channels/{id}. Returns 404 when the channel is
// missing.
func (s *Server) getChannel(w http.ResponseWriter, r *http.Request) {
	userID := streamproxy.UserIDFromContext(r.Context())
	id := chi.URLParam(r, "id")

	ch, err := s.Store.GetChannelView(r.Context(), userID, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErrorMsg(w, http.StatusNotFound, "channel not found")
			return
		}
		s.logger().Warn("get channel failed", "channel_id", id, "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, toChannelDTO(ch))
}
