package streamproxy

import (
	"bufio"
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/RXWatcher/silo-plugin-livetv/internal/httpclient"
	"github.com/RXWatcher/silo-plugin-livetv/internal/store"
)

// segmentPayload is the JSON we sign for each rewritten segment URI. The
// session's secret keys the HMAC so revoking the session also invalidates
// every outstanding segment token (since cookie validation will 404 first,
// and even if it didn't the secret would not match).
type segmentPayload struct {
	URI string `json:"u"`
	Exp int64  `json:"e"`
}

// SignSegment encodes (uri, exp) as base64.RawURL(payload).base64.RawURL(hmac).
// The result is safe to embed in a URL query string without further escaping.
func SignSegment(secret []byte, uri string, expires time.Time) string {
	payload, _ := json.Marshal(segmentPayload{URI: uri, Exp: expires.Unix()})
	pb := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(pb))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return pb + "." + sig
}

// VerifySegment is the inverse of SignSegment. Returns the original URI when
// the signature matches the secret and the expiry has not lapsed.
func VerifySegment(secret []byte, token string) (string, error) {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return "", errors.New("malformed segment token")
	}
	expected := hmac.New(sha256.New, secret)
	expected.Write([]byte(parts[0]))
	want := base64.RawURLEncoding.EncodeToString(expected.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(want), []byte(parts[1])) != 1 {
		return "", errors.New("bad segment signature")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", fmt.Errorf("decode segment payload: %w", err)
	}
	var p segmentPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", fmt.Errorf("decode segment payload json: %w", err)
	}
	if time.Now().Unix() > p.Exp {
		return "", errors.New("segment token expired")
	}
	return p.URI, nil
}

// RewritePlaylist scans body line-by-line, leaves comments and blank lines
// untouched, and rewrites every URI into a signed proxy URL of the form
//
//	<basePath>/stream/<sessionID>/segment?u=<token>
//
// Relative URIs in the upstream playlist are resolved against baseUpstream so
// the signed token always carries an absolute URL the segment handler can use
// without further context.
func RewritePlaylist(body io.Reader, baseUpstream *url.URL, sessionID string, secret []byte, basePath string, ttl time.Duration) ([]byte, error) {
	basePath = strings.TrimRight(basePath, "/")
	if basePath == "" {
		basePath = defaultBasePath
	}
	exp := time.Now().Add(ttl)

	var out bytes.Buffer
	scanner := bufio.NewScanner(body)
	// Generous buffer so long ad-stitched URLs don't overflow the default 64 KiB.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			out.WriteString(line)
			out.WriteByte('\n')
			continue
		}
		abs := trimmed
		if baseUpstream != nil {
			if u, err := baseUpstream.Parse(trimmed); err == nil {
				abs = u.String()
			}
		}
		token := SignSegment(secret, abs, exp)
		fmt.Fprintf(&out, "%s/stream/%s/segment?u=%s\n", basePath, sessionID, url.QueryEscape(token))
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan playlist: %w", err)
	}
	return out.Bytes(), nil
}

// ProxyHLSPlaylist handles GET /api/v1/livetv/stream/{session_id}.m3u8. It
// fetches the upstream playlist with the source's HTTP headers and rewrites
// every URI into a signed proxy URL the client can pull through us.
func (d *Deps) ProxyHLSPlaylist(w http.ResponseWriter, r *http.Request) {
	sessID, sess, ok := d.verifyToken(w, r)
	if !ok {
		return
	}

	urlSess := stripExt(chi.URLParam(r, "session_id"))
	if urlSess != sessID {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	ch, err := d.Store.GetChannel(r.Context(), sess.ChannelID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "channel gone", http.StatusNotFound)
		} else {
			d.logger().Warn("get channel failed", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
		return
	}

	upstreamURL, err := url.Parse(ch.UpstreamURL)
	if err != nil {
		http.Error(w, "bad upstream", http.StatusBadGateway)
		return
	}

	// Fetch the raw upstream playlist, with a short per-channel cache in front
	// so N concurrent viewers of one channel collapse onto a single upstream
	// poll. The cached value is the *raw* upstream body — rewriting (which
	// embeds the per-session signed segment tokens) happens after the cache so
	// each session still gets its own tokens.
	rawBody, ok := d.fetchPlaylistBody(r, ch)
	if !ok {
		http.Error(w, "bad upstream", http.StatusBadGateway)
		return
	}

	rewritten, err := RewritePlaylist(bytes.NewReader(rawBody), upstreamURL, sessID, sess.SessionSecret, d.basePath(), 5*time.Minute)
	if err != nil {
		d.logger().Warn("rewrite playlist failed", "err", err)
		http.Error(w, "rewrite failed", http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(rewritten)

	// Best-effort accounting bump — keeps the session out of the reaper.
	_ = d.Store.UpdateSessionLastByte(r.Context(), sessID, time.Now().UTC(), int64(len(rewritten)))
}

// fetchPlaylistBody returns the raw upstream .m3u8 body for ch, served from a
// short per-channel cache when fresh. On a miss it acquires an upstream
// connection slot, fetches the playlist, caps the read at PlaylistMaxBytes, and
// stores the result. Returns (body, false) on any upstream failure; the caller
// has already written nothing, so it maps the false to a 502.
func (d *Deps) fetchPlaylistBody(r *http.Request, ch store.Channel) ([]byte, bool) {
	cache := d.playlistCacheFor()
	if cached, ok := cache.Get(ch.ID); ok {
		return cached, true
	}

	// Bound concurrent upstream connections. Playlist fetches are short, so we
	// release the slot as soon as the body is read (deferred below).
	if !d.upstreamSemaphore().TryAcquire() {
		d.logger().Warn("upstream at capacity, playlist fetch refused", "channel_id", ch.ID)
		return nil, false
	}
	defer d.upstreamSemaphore().Release()

	headers := d.sourceHeaders(r.Context(), ch.SourceM3UID)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, ch.UpstreamURL, nil)
	if err != nil {
		return nil, false
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := d.httpClient().Do(req)
	if err != nil {
		d.logger().Warn("upstream playlist fetch failed", "err", err)
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, false
	}

	raw, err := io.ReadAll(httpclient.LimitBody(resp.Body, httpclient.PlaylistMaxBytes))
	if err != nil {
		d.logger().Warn("read upstream playlist failed", "err", err)
		return nil, false
	}
	cache.Set(ch.ID, raw)
	return raw, true
}

// ProxyHLSSegment handles GET /api/v1/livetv/stream/{session_id}/segment?u=...
// It validates the token, fetches the upstream segment, and pipes the body
// through to the client. Unlike the MPEG-TS handler this does NOT end the
// session — segment requests are short-lived; the idle reaper takes care of
// truly silent sessions.
func (d *Deps) ProxyHLSSegment(w http.ResponseWriter, r *http.Request) {
	sessID, sess, ok := d.verifyToken(w, r)
	if !ok {
		return
	}
	urlSess := chi.URLParam(r, "session_id")
	if urlSess != sessID {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	token := r.URL.Query().Get("u")
	if token == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	uri, err := VerifySegment(sess.SessionSecret, token)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// Per-user rate limit: segment pulls arrive in a tight cadence; a buggy or
	// hostile client could otherwise hammer this endpoint. The bucket is keyed
	// on the session's user so legitimate playback stays well under the cap.
	if !d.userRateLimiter().Allow(sess.UserID) {
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}

	// Global concurrent-stream cap and upstream-connection cap. Both slots are
	// short-lived for a segment (held only for the fetch + copy) and released
	// on return.
	if !d.streamSemaphore().TryAcquire() {
		http.Error(w, "server at capacity", http.StatusServiceUnavailable)
		return
	}
	defer d.streamSemaphore().Release()

	ch, err := d.Store.GetChannel(r.Context(), sess.ChannelID)
	if err != nil {
		http.Error(w, "channel gone", http.StatusNotFound)
		return
	}
	headers := d.sourceHeaders(r.Context(), ch.SourceM3UID)

	if !d.upstreamSemaphore().TryAcquire() {
		http.Error(w, "upstream at capacity", http.StatusServiceUnavailable)
		return
	}
	defer d.upstreamSemaphore().Release()

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, uri, nil)
	if err != nil {
		http.Error(w, "bad upstream", http.StatusBadGateway)
		return
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := d.httpClient().Do(req)
	if err != nil {
		d.logger().Warn("upstream segment fetch failed", "err", err)
		http.Error(w, "bad upstream", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		http.Error(w, "bad upstream", http.StatusBadGateway)
		return
	}

	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	} else {
		w.Header().Set("Content-Type", "video/mp2t")
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)

	n, err := io.Copy(w, resp.Body)
	if err != nil {
		d.logger().Debug("segment copy ended early", "err", err)
	}
	if n > 0 {
		_ = d.Store.UpdateSessionLastByte(r.Context(), sessID, time.Now().UTC(), n)
	}
}
