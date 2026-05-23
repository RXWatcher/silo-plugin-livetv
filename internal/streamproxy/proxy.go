// Package streamproxy implements the live TV stream proxy: session minting,
// MPEG-TS pass-through, and HLS playlist rewriting plus segment proxying.
//
// Auth model (deviation from the original plan):
//
// The SDK's runtimehost.MintScopedStream RPC requires a numeric MediaFileID
// and is designed for on-demand media; it is NOT a fit for live channels.
// Instead, this plugin mints its own opaque tokens whose authority derives
// from the stream_sessions table it already owns:
//
//   - On session creation we generate a 32-byte random session_secret and
//     store it on the stream_sessions row (Phase 3 schema already has the
//     column).
//   - The cookie / bearer token value is "<session_id>.<hex(session_secret)>".
//     session_id is the lookup key; the hex secret is a constant-time check
//     against the row.
//   - Revocation == setting stream_sessions.ended_at; once ended, subsequent
//     requests find the session "gone" and respond 404.
//
// The stream_sessions.scoped_grant_id column is reused as the opaque token id
// (we store the same value as session_id) so the column stays populated
// without needing a separate runtime-host grant.
package streamproxy

import (
	"context"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"crypto/subtle"

	"github.com/hashicorp/go-hclog"

	"github.com/RXWatcher/silo-plugin-livetv/internal/store"
)

// cookieName is the cookie key that carries the opaque session token. The
// proxy reads it from r.Cookie() in every byte-serving handler.
const cookieName = "livetv_stream"

// defaultBasePath is used when Deps.BasePath is left blank. The plugin mounts
// its public routes under /api/v1/livetv; tests can override.
const defaultBasePath = "/api/v1/livetv"

// sessionCookieTTL is the cookie / token expiry baked into both the
// Set-Cookie Expires field and the JSON `expires_at` response field. 8 hours
// matches the platform's default browser session window.
const sessionCookieTTL = 8 * time.Hour

// Settings exposes only the proxy-relevant knobs from the broader settings
// store. Phase 7 will plug in a DB-backed snapshot; Phase 5 ships with
// StaticSettings for tests and the main wiring.
//
// GuideWindowCap is consumed by the server package's /guide handler; it lives
// here so there's one Settings surface across the whole plugin.
type Settings interface {
	PerUserStreamCap() int
	PerChannelDefaultCap() int
	SessionIdleTimeout() time.Duration
	GuideWindowCap() time.Duration
}

// StaticSettings is a frozen Settings impl handy for tests and the
// transitional main.go wiring. Phase 7 replaces it with a DB-backed snapshot.
type StaticSettings struct {
	PerUser     int
	PerChannel  int
	IdleTimeout time.Duration
	// GuideWindow caps the max guide window served by /guide. When left as
	// the zero value, GuideWindowCap() returns 24h (the Phase 6 default).
	GuideWindow time.Duration
}

// PerUserStreamCap returns the max concurrent sessions a single user may run.
func (s StaticSettings) PerUserStreamCap() int { return s.PerUser }

// PerChannelDefaultCap returns the default per-channel concurrency cap.
func (s StaticSettings) PerChannelDefaultCap() int { return s.PerChannel }

// SessionIdleTimeout returns the duration after which the idle reaper ends
// silent sessions.
func (s StaticSettings) SessionIdleTimeout() time.Duration { return s.IdleTimeout }

// GuideWindowCap returns the hard cap applied to GET /guide windows. Zero
// values fall back to 24h so tests that don't care don't have to set it.
func (s StaticSettings) GuideWindowCap() time.Duration {
	if s.GuideWindow <= 0 {
		return 24 * time.Hour
	}
	return s.GuideWindow
}

// Deps is the dependency bundle shared by every stream-proxy handler. It is
// instantiated once in main and method-bound to the chi router; tests build
// their own to inject httptest servers and in-memory settings.
type Deps struct {
	Store    *store.Store
	Settings Settings
	Logger   hclog.Logger
	// HTTP is used for all upstream calls (probe + proxy). nil → http.DefaultClient.
	HTTP *http.Client
	// BasePath is the public mount point (e.g. "/api/v1/livetv"). Defaults to
	// defaultBasePath when empty.
	BasePath string
}

// httpClient returns the configured upstream client, falling back to
// http.DefaultClient.
func (d *Deps) httpClient() *http.Client {
	if d.HTTP != nil {
		return d.HTTP
	}
	return http.DefaultClient
}

// logger returns the configured logger, falling back to a null logger so
// tests don't have to wire one in.
func (d *Deps) logger() hclog.Logger {
	if d.Logger != nil {
		return d.Logger
	}
	return hclog.NewNullLogger()
}

// basePath returns the public mount point, normalised to omit any trailing slash.
func (d *Deps) basePath() string {
	bp := d.BasePath
	if bp == "" {
		bp = defaultBasePath
	}
	return strings.TrimRight(bp, "/")
}

// ctxKey is unexported so callers can't collide on the same context value.
type ctxKey int

const userIDKey ctxKey = 0

// UserIDFromContext extracts the userID set by upstream auth middleware. The
// proxy itself does not authenticate users — that's the host's job.
func UserIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(userIDKey).(string)
	return v
}

// WithUserID attaches userID to ctx. Used by tests and by the placeholder
// middleware in main.go until Phase 6 finalises the auth wiring.
func WithUserID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, userIDKey, id)
}

// tokenValue returns the cookie/bearer value for a session pair.
// Format: "<session_id>.<hex(secret)>".
func tokenValue(sessionID string, secret []byte) string {
	return sessionID + "." + hex.EncodeToString(secret)
}

// splitToken parses the "<session_id>.<hex(secret)>" form. Returns
// (sessionID, secret) when both are present; otherwise an error.
func splitToken(value string) (string, []byte, error) {
	parts := strings.SplitN(value, ".", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", nil, errors.New("malformed token")
	}
	secret, err := hex.DecodeString(parts[1])
	if err != nil {
		return "", nil, errors.New("malformed token secret")
	}
	return parts[0], secret, nil
}

// extractToken pulls the token value either from the dedicated cookie or
// from an Authorization: Bearer header. Cookie wins when both are present.
func extractToken(r *http.Request) (string, bool) {
	if c, err := r.Cookie(cookieName); err == nil && c.Value != "" {
		return c.Value, true
	}
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		v := strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
		if v != "" {
			return v, true
		}
	}
	return "", false
}

// verifyToken validates the cookie/bearer token on r against a live session
// row. On success it returns (sessionID, session, true). On any failure it
// writes the appropriate response (401 for auth issues, 404 for ended /
// missing sessions) and returns (_, _, false).
func (d *Deps) verifyToken(w http.ResponseWriter, r *http.Request) (string, *store.Session, bool) {
	raw, ok := extractToken(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return "", nil, false
	}
	sessID, secret, err := splitToken(raw)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return "", nil, false
	}
	sess, err := d.Store.GetSession(r.Context(), sessID)
	if err != nil {
		http.Error(w, "session gone", http.StatusNotFound)
		return "", nil, false
	}
	if sess.EndedAt != nil {
		http.Error(w, "session gone", http.StatusNotFound)
		return "", nil, false
	}
	if subtle.ConstantTimeCompare(secret, sess.SessionSecret) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return "", nil, false
	}
	return sessID, &sess, true
}
