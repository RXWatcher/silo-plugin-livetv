package server

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/RXWatcher/silo-plugin-livetv/internal/streamproxy"
)

// reorderRequest is the POST /favorites/reorder body.
type reorderRequest struct {
	ChannelIDs []string `json:"channel_ids"`
}

// listFavorites handles GET /favorites.
func (s *Server) listFavorites(w http.ResponseWriter, r *http.Request) {
	userID := streamproxy.UserIDFromContext(r.Context())
	favs, err := s.Store.ListFavorites(r.Context(), userID)
	if err != nil {
		s.logger().Warn("list favorites failed", "user_id", userID, "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	dtos := make([]favoriteDTO, len(favs))
	for i, f := range favs {
		dtos[i] = favoriteDTO{ChannelID: f.ChannelID, Position: f.Position}
	}
	writeJSON(w, http.StatusOK, listEnvelope[favoriteDTO]{Data: dtos})
}

// addFavorite handles POST /favorites/{channel_id}. Idempotent in the store
// layer; we always respond 204 on success.
func (s *Server) addFavorite(w http.ResponseWriter, r *http.Request) {
	userID := streamproxy.UserIDFromContext(r.Context())
	id := chi.URLParam(r, "channel_id")
	if id == "" {
		writeErrorMsg(w, http.StatusBadRequest, "channel_id required")
		return
	}
	if err := s.Store.AddFavorite(r.Context(), userID, id); err != nil {
		s.logger().Warn("add favorite failed", "user_id", userID, "channel_id", id, "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// removeFavorite handles DELETE /favorites/{channel_id}. Idempotent.
func (s *Server) removeFavorite(w http.ResponseWriter, r *http.Request) {
	userID := streamproxy.UserIDFromContext(r.Context())
	id := chi.URLParam(r, "channel_id")
	if id == "" {
		writeErrorMsg(w, http.StatusBadRequest, "channel_id required")
		return
	}
	if err := s.Store.RemoveFavorite(r.Context(), userID, id); err != nil {
		s.logger().Warn("remove favorite failed", "user_id", userID, "channel_id", id, "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// reorderFavorites handles POST /favorites/reorder. Body shape:
//
//	{"channel_ids": ["id1", "id2", ...]}
//
// The store call silently ignores channels the user has not favorited; at the
// API layer we explicitly reject any unknown id with 400 so callers don't
// silently lose intent.
func (s *Server) reorderFavorites(w http.ResponseWriter, r *http.Request) {
	userID := streamproxy.UserIDFromContext(r.Context())
	var req reorderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorMsg(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if len(req.ChannelIDs) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Validate that every channel in the request body is already a favorite of
	// this user — the store's reorder pass-through silently drops unknowns.
	favs, err := s.Store.ListFavorites(r.Context(), userID)
	if err != nil {
		s.logger().Warn("reorder: list favorites failed", "user_id", userID, "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	known := make(map[string]struct{}, len(favs))
	for _, f := range favs {
		known[f.ChannelID] = struct{}{}
	}
	for _, id := range req.ChannelIDs {
		if _, ok := known[id]; !ok {
			writeErrorMsg(w, http.StatusBadRequest, "channel "+id+" is not a favorite")
			return
		}
	}

	if err := s.Store.ReorderFavorites(r.Context(), userID, req.ChannelIDs); err != nil {
		s.logger().Warn("reorder favorites failed", "user_id", userID, "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
