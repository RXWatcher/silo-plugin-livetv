package server

import (
	"net/http"
	"time"

	"github.com/RXWatcher/silo-plugin-livetv/internal/streamproxy"
)

// guideRange is the JSON shape for the window field of the guide response.
type guideRange struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// guideWindowOut is the wire shape for GET /guide. Keys in Data are channel
// ids (NOT xmltv channel ids) — the store's GuideWindow join already does the
// reverse mapping via channel_epg_keys.
type guideWindowOut struct {
	Data   map[string][]programDTO `json:"data"`
	Window guideRange              `json:"window"`
}

// listGroups handles GET /groups. Returns the distinct effective group titles
// for enabled channels.
func (s *Server) listGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := s.Store.ListGroups(r.Context())
	if err != nil {
		s.logger().Warn("list groups failed", "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if groups == nil {
		groups = []string{}
	}
	writeJSON(w, http.StatusOK, listEnvelope[string]{Data: groups})
}

// guideWindow handles GET /guide. Query params:
//   - start, end: RFC3339 (default now → now+4h)
//   - channels:   repeated channel id; defaults to every visible channel
//   - group:      narrows the default channel set
//
// The window is hard-capped via Settings.GuideWindowCap (24h in Phase 6 default)
// so a single request can't sweep the entire programs table.
func (s *Server) guideWindow(w http.ResponseWriter, r *http.Request) {
	userID := streamproxy.UserIDFromContext(r.Context())
	q := r.URL.Query()

	start, _ := time.Parse(time.RFC3339, q.Get("start"))
	end, _ := time.Parse(time.RFC3339, q.Get("end"))
	if start.IsZero() {
		start = time.Now().UTC()
	}
	if end.IsZero() {
		end = start.Add(4 * time.Hour)
	}
	if end.Before(start) {
		writeErrorMsg(w, http.StatusBadRequest, "end must be after start")
		return
	}
	cap := guideCap(s.Settings)
	if cap > 0 && end.Sub(start) > cap {
		end = start.Add(cap)
	}

	channels := q["channels"]
	if len(channels) == 0 {
		ids, err := s.Store.VisibleChannelIDsForUser(r.Context(), userID, q.Get("group"))
		if err != nil {
			s.logger().Warn("visible channels failed", "user_id", userID, "err", err)
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		channels = ids
	}

	rows, err := s.Store.GuideWindow(r.Context(), channels, start, end)
	if err != nil {
		s.logger().Warn("guide window failed", "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	out := guideWindowOut{
		Data:   make(map[string][]programDTO, len(rows)),
		Window: guideRange{Start: start, End: end},
	}
	for cid, progs := range rows {
		dtos := make([]programDTO, len(progs))
		for i, p := range progs {
			dtos[i] = toProgramDTO(p, cid)
		}
		out.Data[cid] = dtos
	}
	writeJSON(w, http.StatusOK, out)
}

// guideCap returns the configured guide-window cap. When the Settings impl is
// nil (e.g. minimal test setups), we fall back to 24h to match the Phase 6
// spec.
func guideCap(settings streamproxy.Settings) time.Duration {
	const defaultCap = 24 * time.Hour
	if settings == nil {
		return defaultCap
	}
	if v := settings.GuideWindowCap(); v > 0 {
		return v
	}
	return defaultCap
}
