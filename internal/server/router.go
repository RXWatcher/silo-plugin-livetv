package server

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// Routes returns the fully composed http.Handler for the plugin. The shape:
//
//	{BasePath}/healthz                              public
//	{BasePath}/channels                              session
//	{BasePath}/channels/{id}                         session
//	{BasePath}/groups                                session
//	{BasePath}/guide                                 session
//	{BasePath}/programs/{id}                         session
//	{BasePath}/programs/search                       session
//	{BasePath}/favorites                             session
//	{BasePath}/favorites/{channel_id}                session
//	{BasePath}/favorites/reorder                     session
//	{BasePath}/recent                                session
//	{BasePath}/channels/{id}/stream                  session (mints session)
//	{BasePath}/stream/{session_id}.ts                token cookie
//	{BasePath}/stream/{session_id}.m3u8              token cookie
//	{BasePath}/stream/{session_id}/segment           token cookie
//	{BasePath}/admin/*                               admin (Phase 7)
//
// The user/admin routes funnel through RequireSession / RequireAdmin so the
// X-Continuum-User-Id header is reflected onto request context. The
// stream-byte routes deliberately skip that middleware because they
// authenticate via the opaque session cookie set by CreateSession.
func (s *Server) Routes() http.Handler {
	r := chi.NewRouter()
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	base := s.basePath()
	r.Route(base, func(api chi.Router) {
		// User-scoped API (RequireSession reflects X-Continuum-User-Id).
		api.Group(func(u chi.Router) {
			u.Use(RequireSession)
			u.Get("/channels", s.listChannels)
			u.Get("/channels/{id}", s.getChannel)
			u.Get("/groups", s.listGroups)
			u.Get("/guide", s.guideWindow)
			u.Get("/programs/search", s.searchPrograms)
			u.Get("/programs/{id}", s.getProgram)
			u.Get("/favorites", s.listFavorites)
			u.Post("/favorites/reorder", s.reorderFavorites)
			u.Post("/favorites/{channel_id}", s.addFavorite)
			u.Delete("/favorites/{channel_id}", s.removeFavorite)
			u.Get("/recent", s.listRecent)
			if s.Stream != nil {
				u.Post("/channels/{id}/stream", s.Stream.CreateSession)
			}
		})

		// Stream byte routes: cookie/bearer auth, no session header needed.
		if s.Stream != nil {
			api.Get("/stream/{session_id}.ts", s.Stream.ProxyMPEGTS)
			api.Get("/stream/{session_id}.m3u8", s.Stream.ProxyHLSPlaylist)
			api.Get("/stream/{session_id}/segment", s.Stream.ProxyHLSSegment)
		}

		// Admin subrouter — gated by RequireAdmin. Sub-mounts live in
		// admin_sources.go so the route table here stays a quick directory
		// rather than a wall of handlers.
		api.Route("/admin", func(adm chi.Router) {
			adm.Use(RequireAdmin)
			s.mountAdminSources(adm)
		})
	})

	return r
}
