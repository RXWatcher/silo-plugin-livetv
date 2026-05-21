package streamproxy

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"

	"github.com/RXWatcher/continuum-plugin-livetv/internal/store"
)

// createSessionResponse is the JSON body returned by POST /channels/{id}/stream.
type createSessionResponse struct {
	SessionID   string    `json:"session_id"`
	PlaybackURL string    `json:"playback_url"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// errorJSON is the shape of every JSON error body emitted by the proxy.
type errorJSON struct {
	Error errorBody `json:"error"`
}
type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// writeJSONError sends a structured JSON error with the given HTTP status.
func writeJSONError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorJSON{Error: errorBody{Code: code, Message: msg}})
}

// CreateSession handles POST /api/v1/livetv/channels/{id}/stream. It validates
// caps, probes the upstream kind when unknown, mints a session row, returns
// the playback URL, and sets the auth cookie. See proxy.go for the auth model.
func (d *Deps) CreateSession(w http.ResponseWriter, r *http.Request) {
	userID := UserIDFromContext(r.Context())
	if userID == "" {
		writeJSONError(w, http.StatusUnauthorized, "unauthenticated", "missing user")
		return
	}

	channelID := chi.URLParam(r, "id")
	if channelID == "" {
		writeJSONError(w, http.StatusNotFound, "channel_not_found", "unknown channel")
		return
	}

	ch, err := d.Store.GetChannel(r.Context(), channelID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "channel_not_found", "unknown channel")
			return
		}
		d.logger().Warn("get channel failed", "channel_id", channelID, "err", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "channel lookup failed")
		return
	}
	if !ch.EffectiveEnabled {
		// Disabled (either source dropped it or admin flipped enabled_admin=false).
		writeJSONError(w, http.StatusNotFound, "channel_not_found", "unknown channel")
		return
	}

	// Per-user concurrency cap.
	if userCap := d.Settings.PerUserStreamCap(); userCap > 0 {
		n, err := d.Store.CountActiveByUser(r.Context(), userID)
		if err != nil {
			d.logger().Warn("count active by user failed", "user_id", userID, "err", err)
			writeJSONError(w, http.StatusInternalServerError, "internal_error", "cap check failed")
			return
		}
		if n >= userCap {
			writeJSONError(w, http.StatusTooManyRequests, "user_cap_exceeded",
				fmt.Sprintf("user stream cap %d reached", userCap))
			return
		}
	}
	// Per-channel concurrency cap.
	if chanCap := d.Settings.PerChannelDefaultCap(); chanCap > 0 {
		n, err := d.Store.CountActiveByChannel(r.Context(), channelID)
		if err != nil {
			d.logger().Warn("count active by channel failed", "channel_id", channelID, "err", err)
			writeJSONError(w, http.StatusInternalServerError, "internal_error", "cap check failed")
			return
		}
		if n >= chanCap {
			writeJSONError(w, http.StatusTooManyRequests, "channel_cap_exceeded",
				fmt.Sprintf("channel stream cap %d reached", chanCap))
			return
		}
	}

	// Resolve upstream kind. If unknown, probe and persist the result.
	kind := ch.UpstreamKind
	if kind == "" || kind == "unknown" {
		var srcHeaders map[string]string
		if ch.SourceM3UID != "" {
			if src, err := d.Store.GetM3USource(r.Context(), ch.SourceM3UID); err == nil {
				srcHeaders = src.HTTPHeaders
			}
		}
		detected, err := d.detectUpstreamKind(r.Context(), ch.UpstreamURL, srcHeaders)
		if err != nil {
			d.logger().Warn("probe upstream failed", "channel_id", channelID, "err", err)
			detected = "mpegts"
		}
		kind = detected
		if err := d.Store.SetUpstreamKind(r.Context(), channelID, kind); err != nil {
			d.logger().Warn("persist upstream kind failed", "channel_id", channelID, "err", err)
		}
	}

	// Mint the session secret.
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		d.logger().Error("rand.Read failed", "err", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "secret mint failed")
		return
	}

	// Reserve the session id up front so scoped_grant_id == session_id (we
	// reuse that column as the opaque token id — see proxy.go header comment).
	sessID := storeNewULID()
	sess := store.Session{
		ID:            sessID,
		UserID:        userID,
		ChannelID:     channelID,
		ScopedGrantID: sessID,
		SessionSecret: secret,
		ClientIP:      clientIP(r),
		UserAgent:     r.UserAgent(),
	}
	if _, err := d.Store.CreateSession(r.Context(), sess); err != nil {
		d.logger().Error("create session failed", "err", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "session mint failed")
		return
	}

	// Best-effort recents update (don't fail the call if this errors).
	if err := d.Store.MarkTuned(r.Context(), userID, channelID); err != nil {
		d.logger().Warn("mark tuned failed", "err", err)
	}

	// Cookie + JSON response.
	value := tokenValue(sessID, secret)
	expires := time.Now().Add(sessionCookieTTL).UTC()
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    value,
		Path:     d.basePath() + "/stream/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		Expires:  expires,
	})

	var playbackURL string
	if kind == "hls" {
		playbackURL = d.basePath() + "/stream/" + sessID + ".m3u8"
	} else {
		playbackURL = d.basePath() + "/stream/" + sessID + ".ts"
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(createSessionResponse{
		SessionID:   sessID,
		PlaybackURL: playbackURL,
		ExpiresAt:   expires,
	})
}

// detectUpstreamKind tries to classify a URL as "hls" or "mpegts". The order:
//  1. URL extension shortcut (.m3u8 / .ts / .mpegts).
//  2. HEAD probe of Content-Type.
//  3. GET first ~512 bytes and sniff the body (MPEG-TS sync byte or #EXTM3U).
//
// On unrecoverable ambiguity it returns "mpegts" with a warning log — that's
// the larger of the two formats and the safer default for hardware decoders.
func (d *Deps) detectUpstreamKind(ctx context.Context, rawURL string, headers map[string]string) (string, error) {
	lower := strings.ToLower(rawURL)
	// Strip query string for extension sniffing only.
	if i := strings.IndexByte(lower, '?'); i >= 0 {
		lower = lower[:i]
	}
	switch {
	case strings.HasSuffix(lower, ".m3u8"):
		return "hls", nil
	case strings.HasSuffix(lower, ".ts"), strings.HasSuffix(lower, ".mpegts"):
		return "mpegts", nil
	}

	// HEAD probe.
	if kind, ok := d.headProbe(ctx, rawURL, headers); ok {
		return kind, nil
	}

	// GET sniff: read up to 512 bytes.
	return d.getSniff(ctx, rawURL, headers)
}

// headProbe issues an HTTP HEAD and inspects Content-Type. Returns (kind, true)
// only on a confident match; otherwise (_, false) so the caller can try a GET.
func (d *Deps) headProbe(ctx context.Context, rawURL string, headers map[string]string) (string, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, rawURL, nil)
	if err != nil {
		return "", false
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := d.httpClient().Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return "", false
	}
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	switch {
	case strings.Contains(ct, "application/vnd.apple.mpegurl"),
		strings.Contains(ct, "application/x-mpegurl"):
		return "hls", true
	case strings.Contains(ct, "video/mp2t"),
		strings.Contains(ct, "video/mpeg"):
		return "mpegts", true
	}
	return "", false
}

// getSniff issues a small GET and inspects the body for an MPEG-TS sync byte
// at offset 0 (the format's defining signature) or an #EXTM3U prelude (HLS).
// Returns "mpegts" as a logged default when neither matches.
func (d *Deps) getSniff(ctx context.Context, rawURL string, headers map[string]string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "mpegts", err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	// Range request so well-behaved upstreams stream us a fraction of a packet.
	req.Header.Set("Range", "bytes=0-511")
	resp, err := d.httpClient().Do(req)
	if err != nil {
		return "mpegts", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return "mpegts", fmt.Errorf("sniff http %d", resp.StatusCode)
	}
	buf := make([]byte, 512)
	n, _ := io.ReadFull(resp.Body, buf)
	buf = buf[:n]
	if n > 0 && buf[0] == 0x47 {
		return "mpegts", nil
	}
	if bytes.HasPrefix(bytes.TrimLeft(buf, " \t\r\n"), []byte("#EXTM3U")) {
		return "hls", nil
	}
	d.logger().Warn("upstream sniff inconclusive, defaulting to mpegts", "url", rawURL)
	return "mpegts", nil
}

// clientIP returns the most useful textual client IP for the session row.
// Prefers X-Forwarded-For (first hop) when present, falling back to
// RemoteAddr without its port.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// Take the first comma-separated value.
		if i := strings.IndexByte(xff, ','); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	addr := r.RemoteAddr
	if i := strings.LastIndexByte(addr, ':'); i > 0 {
		// IPv6 addrs may be wrapped in [].
		host := addr[:i]
		host = strings.TrimPrefix(host, "[")
		host = strings.TrimSuffix(host, "]")
		return host
	}
	return addr
}

// storeNewULID mints a fresh ULID for the session row. We reserve the id
// up front so we can set both stream_sessions.id and stream_sessions.scoped_grant_id
// to the same value (see proxy.go header comment for why we reuse the column).
func storeNewULID() string {
	return ulid.MustNew(ulid.Timestamp(time.Now()), rand.Reader).String()
}
