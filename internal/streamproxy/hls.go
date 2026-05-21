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

	"github.com/ContinuumApp/continuum-plugin-livetv/internal/store"
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

	headers := d.sourceHeaders(r.Context(), ch.SourceM3UID)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, ch.UpstreamURL, nil)
	if err != nil {
		http.Error(w, "bad upstream", http.StatusBadGateway)
		return
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := d.httpClient().Do(req)
	if err != nil {
		d.logger().Warn("upstream playlist fetch failed", "err", err)
		http.Error(w, "bad upstream", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		http.Error(w, "bad upstream", http.StatusBadGateway)
		return
	}

	rewritten, err := RewritePlaylist(resp.Body, upstreamURL, sessID, sess.SessionSecret, d.basePath(), 5*time.Minute)
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

	ch, err := d.Store.GetChannel(r.Context(), sess.ChannelID)
	if err != nil {
		http.Error(w, "channel gone", http.StatusNotFound)
		return
	}
	headers := d.sourceHeaders(r.Context(), ch.SourceM3UID)

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
