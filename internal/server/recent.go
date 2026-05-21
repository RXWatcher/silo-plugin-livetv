package server

import (
	"net/http"

	"github.com/RXWatcher/continuum-plugin-livetv/internal/streamproxy"
)

// listRecent handles GET /recent. Defaults limit=20, max 100.
func (s *Server) listRecent(w http.ResponseWriter, r *http.Request) {
	userID := streamproxy.UserIDFromContext(r.Context())
	limit := clampLimit(r.URL.Query().Get("limit"), 20, 100)

	rows, err := s.Store.ListRecent(r.Context(), userID, limit)
	if err != nil {
		s.logger().Warn("list recent failed", "user_id", userID, "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	dtos := make([]recentDTO, len(rows))
	for i, rec := range rows {
		dtos[i] = recentDTO{ChannelID: rec.ChannelID, LastTunedAt: rec.LastTunedAt}
	}
	writeJSON(w, http.StatusOK, listEnvelope[recentDTO]{Data: dtos})
}
