package streamproxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/RXWatcher/silo-plugin-livetv/internal/store"
)

// proxyBufSize is the chunk we pump from upstream to client. 64 KiB is large
// enough that we're not syscall-bound on a typical MPEG-TS but small enough
// that the client sees prompt flushes.
const proxyBufSize = 64 * 1024

// byteCounterFlushInterval is the minimum delay between database updates of
// stream_sessions.last_byte_at / bytes_streamed. The reaper polls every few
// seconds so 5s is the right balance between freshness and DB write rate.
const byteCounterFlushInterval = 5 * time.Second

// ProxyMPEGTS handles GET /api/v1/livetv/stream/{session_id}.ts. It validates
// the session, opens an upstream connection, and streams body-for-body to the
// client until either side hangs up. Idle accounting is debounced (5s) into the
// stream_sessions row so the reaper can drop silent sessions.
func (d *Deps) ProxyMPEGTS(w http.ResponseWriter, r *http.Request) {
	sessID, sess, ok := d.verifyToken(w, r)
	if !ok {
		return
	}

	// URL-side session id must match the cookie. Mismatch suggests a stolen /
	// mis-routed cookie; reject before we leak bytes.
	urlSess := stripExt(chi.URLParam(r, "session_id"))
	if urlSess != sessID {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// Global concurrent-stream cap: bound total in-flight client streams so a
	// surge can't exhaust host file descriptors / memory.
	if !d.streamSemaphore().TryAcquire() {
		http.Error(w, "server at capacity", http.StatusServiceUnavailable)
		return
	}
	defer d.streamSemaphore().Release()

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

	headers := d.sourceHeaders(r.Context(), ch.SourceM3UID)

	// Global upstream-connection cap. The MPEG-TS pass-through holds one
	// upstream socket for the whole session, so we keep the slot for the
	// duration of the pump and release it when the stream ends.
	if !d.upstreamSemaphore().TryAcquire() {
		http.Error(w, "upstream at capacity", http.StatusServiceUnavailable)
		return
	}
	defer d.upstreamSemaphore().Release()

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, ch.UpstreamURL, nil)
	if err != nil {
		_ = d.Store.EndSession(r.Context(), sessID, "upstream_error")
		http.Error(w, "bad upstream", http.StatusBadGateway)
		return
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := d.httpClient().Do(req)
	if err != nil {
		_ = d.Store.EndSession(r.Context(), sessID, "upstream_error")
		d.logger().Warn("upstream get failed", "err", err)
		http.Error(w, "bad upstream", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_ = d.Store.EndSession(r.Context(), sessID, "upstream_error")
		d.logger().Warn("upstream non-2xx", "status", resp.StatusCode)
		http.Error(w, "bad upstream", http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "video/mp2t")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)

	// flusher is nil for http.Recorder in tests, which is fine — we only call
	// Flush() when it's non-nil.
	flusher, _ := w.(http.Flusher)

	reason, total := d.pumpBody(r, w, resp.Body, flusher, sessID)
	// Final accounting flush before ending the row.
	if total > 0 {
		_ = d.Store.UpdateSessionLastByte(r.Context(), sessID, time.Now().UTC(), 0)
	}
	if err := d.Store.EndSession(r.Context(), sessID, reason); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		d.logger().Debug("end session failed", "err", err)
	}
}

// pumpBody streams from src to dst in proxyBufSize chunks, flushing each write
// to the client and debouncing DB byte-counter updates. Returns the end-reason
// for the session ("client_disconnect" on clean EOF / closed client, or
// "upstream_error" on a mid-stream upstream failure) plus the total bytes
// streamed.
func (d *Deps) pumpBody(r *http.Request, dst io.Writer, src io.Reader, flusher http.Flusher, sessID string) (string, int64) {
	buf := make([]byte, proxyBufSize)
	var (
		total     int64
		batched   int64
		lastFlush = time.Now()
	)
	for {
		// Honour client cancellation early.
		if err := r.Context().Err(); err != nil {
			d.flushCounter(r, sessID, batched, true)
			return "client_disconnect", total
		}
		n, readErr := src.Read(buf)
		if n > 0 {
			if _, writeErr := dst.Write(buf[:n]); writeErr != nil {
				// Client hung up.
				d.flushCounter(r, sessID, batched, true)
				return "client_disconnect", total
			}
			if flusher != nil {
				flusher.Flush()
			}
			total += int64(n)
			batched += int64(n)
			if time.Since(lastFlush) >= byteCounterFlushInterval {
				d.flushCounter(r, sessID, batched, false)
				batched = 0
				lastFlush = time.Now()
			}
		}
		if readErr != nil {
			d.flushCounter(r, sessID, batched, true)
			if errors.Is(readErr, io.EOF) {
				return "client_disconnect", total
			}
			if errors.Is(readErr, io.ErrUnexpectedEOF) {
				return "client_disconnect", total
			}
			// Any other error mid-stream we treat as an upstream failure.
			return "upstream_error", total
		}
	}
}

// flushCounter writes the debounced byte tally to stream_sessions. When isFinal
// is true we want the lastByteAt timestamp pegged to "now" even if no bytes
// were streamed in the final batch (so the reaper doesn't immediately end an
// in-progress request).
func (d *Deps) flushCounter(r *http.Request, sessID string, bytes int64, isFinal bool) {
	if bytes == 0 && !isFinal {
		return
	}
	// Use a detached context for the final flush so an already-cancelled
	// request context (the common case for client_disconnect) doesn't drop
	// the update.
	ctx := r.Context()
	if isFinal {
		ctx = context.Background()
	}
	if err := d.Store.UpdateSessionLastByte(ctx, sessID, time.Now().UTC(), bytes); err != nil &&
		!errors.Is(err, store.ErrNotFound) {
		d.logger().Debug("update last byte failed", "err", err)
	}
}

// stripExt drops the trailing ".ts" or ".m3u8" from a session id URL parameter
// so the handler can compare it against the cookie's session id.
func stripExt(s string) string {
	for _, ext := range []string{".ts", ".m3u8", ".mpegts"} {
		if l := len(s) - len(ext); l > 0 && s[l:] == ext {
			return s[:l]
		}
	}
	return s
}

// sourceHeaders returns the http_headers map for the channel's parent m3u
// source, or nil if the source is missing. Used by every upstream HTTP call so
// User-Agent / Referer overrides propagate.
func (d *Deps) sourceHeaders(ctx context.Context, sourceID string) map[string]string {
	if sourceID == "" {
		return nil
	}
	src, err := d.Store.GetM3USource(ctx, sourceID)
	if err != nil {
		return nil
	}
	return src.HTTPHeaders
}
