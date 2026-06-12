package server

import (
	"net/http"

	"github.com/RXWatcher/silo-plugin-livetv/internal/streamproxy"
)

// Header names used by the host to authenticate the bridged HTTP request.
// The portal sets X-Silo-User-Id on every request; admin-scoped routes
// additionally require X-Silo-User-Role: admin (the host stamps the role of
// the requesting account on every proxied request).
//
// Phase 6 keeps the implementation deliberately simple — the host has already
// validated the user identity by the time the request reaches the plugin, so
// the middleware just reflects the header into request context. Later phases
// may swap in JWT validation here without touching the handlers.
const (
	headerUserID = "X-Silo-User-Id"
	headerRole   = "X-Silo-User-Role"
	roleAdmin    = "admin"
)

// RequireSession is the user-scoped auth middleware. Routes wrapped with it
// require X-Silo-User-Id; the userID is attached to the request context
// via streamproxy.WithUserID so handlers can read it with UserIDFromContext.
func RequireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uid := r.Header.Get(headerUserID)
		if uid == "" {
			writeErrorMsg(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r.WithContext(streamproxy.WithUserID(r.Context(), uid)))
	})
}

// RequireAdmin gates the /admin/* subrouter. It still relies on
// X-Silo-User-Id for identity, but additionally demands
// X-Silo-User-Role: admin (the host stamps this header with the requesting
// account's role on every proxied request).
func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uid := r.Header.Get(headerUserID)
		if uid == "" {
			writeErrorMsg(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if r.Header.Get(headerRole) != roleAdmin {
			writeErrorMsg(w, http.StatusForbidden, "admin only")
			return
		}
		next.ServeHTTP(w, r.WithContext(streamproxy.WithUserID(r.Context(), uid)))
	})
}
