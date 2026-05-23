package server

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/RXWatcher/silo-plugin-livetv/internal/store"
)

// getProgram handles GET /programs/{id}. The store.Program returned by
// GetProgram already carries credits in original Position order.
func (s *Server) getProgram(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	p, err := s.Store.GetProgram(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErrorMsg(w, http.StatusNotFound, "program not found")
			return
		}
		s.logger().Warn("get program failed", "program_id", id, "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, toProgramDTO(p, ""))
}

// searchPrograms handles GET /programs/search. Query params:
//   - q:    required free-text needle (matched against title/sub_title/description)
//   - from: RFC3339 lower bound, defaults to now()
//   - to:   RFC3339 upper bound, defaults to from+48h
//   - limit: 1..500, default 100
//
// An empty q short-circuits to {"data":[]}. Anything else falls through to
// store.SearchPrograms.
func (s *Server) searchPrograms(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	needle := q.Get("q")
	if needle == "" {
		writeJSON(w, http.StatusOK, listEnvelope[programDTO]{Data: []programDTO{}})
		return
	}

	from, _ := time.Parse(time.RFC3339, q.Get("from"))
	to, _ := time.Parse(time.RFC3339, q.Get("to"))
	if from.IsZero() {
		from = time.Now().UTC()
	}
	if to.IsZero() {
		to = from.Add(48 * time.Hour)
	}
	if to.Before(from) {
		writeErrorMsg(w, http.StatusBadRequest, "to must be after from")
		return
	}

	limit := clampLimit(q.Get("limit"), 100, 500)

	hits, err := s.Store.SearchPrograms(r.Context(), needle, from, to, limit)
	if err != nil {
		s.logger().Warn("search programs failed", "q", needle, "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	dtos := make([]programDTO, len(hits))
	for i, p := range hits {
		dtos[i] = toProgramDTO(p, "")
	}
	writeJSON(w, http.StatusOK, listEnvelope[programDTO]{Data: dtos})
}
