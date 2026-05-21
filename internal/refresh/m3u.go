// Package refresh implements the background workers that pull M3U playlists
// and XMLTV guide data from upstream providers and persist them through the
// store. It also hosts the idle-session reaper used by the scheduler.
package refresh

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/hashicorp/go-hclog"

	"github.com/RXWatcher/continuum-plugin-livetv/internal/m3u"
	"github.com/RXWatcher/continuum-plugin-livetv/internal/store"
)

// M3UWorker pulls M3U playlists from configured upstream providers, upserts
// channels into the store, and soft-disables channels absent from the latest
// refresh. Conditional GET (ETag / Last-Modified) keeps mutation out of the
// hot path when the upstream is unchanged.
type M3UWorker struct {
	Store  *store.Store
	Client *http.Client
	Logger hclog.Logger
}

// httpClient returns the worker's HTTP client, falling back to
// http.DefaultClient when none was supplied.
func (w *M3UWorker) httpClient() *http.Client {
	if w.Client != nil {
		return w.Client
	}
	return http.DefaultClient
}

// logger returns the worker's hclog.Logger, falling back to a null logger.
func (w *M3UWorker) logger() hclog.Logger {
	if w.Logger != nil {
		return w.Logger
	}
	return hclog.NewNullLogger()
}

// RefreshAll iterates every enabled M3U source and refreshes it in sequence.
// A per-source error is logged but does not abort the remaining sources; the
// individual errors are joined and returned so callers (typically the
// scheduler) can surface the failure to the host without losing detail.
func (w *M3UWorker) RefreshAll(ctx context.Context) error {
	srcs, err := w.Store.ListM3USources(ctx)
	if err != nil {
		return fmt.Errorf("list m3u sources: %w", err)
	}
	var errs []error
	for _, src := range srcs {
		if !src.Enabled {
			continue
		}
		if err := w.RefreshOne(ctx, src.ID); err != nil {
			w.logger().Warn("m3u refresh failed", "source", src.ID, "name", src.Name, "err", err)
			errs = append(errs, fmt.Errorf("source %s: %w", src.ID, err))
		}
	}
	return errors.Join(errs...)
}

// RefreshOne refreshes a single M3U source by id, applying conditional-GET
// semantics, persisting any resulting channel batch, and marking the source's
// last-status / etag / last-modified fields. Returns an error for transport,
// HTTP, parse, or persistence failures; 304 Not Modified is success.
func (w *M3UWorker) RefreshOne(ctx context.Context, id string) error {
	src, err := w.Store.GetM3USource(ctx, id)
	if err != nil {
		return fmt.Errorf("get m3u source: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src.URL, nil)
	if err != nil {
		now := time.Now().UTC()
		_ = w.Store.MarkM3UStatus(ctx, id, "error: "+err.Error(), src.ETag, src.LastModified, now)
		return fmt.Errorf("build request: %w", err)
	}
	for k, v := range src.HTTPHeaders {
		req.Header.Set(k, v)
	}
	if src.ETag != "" {
		req.Header.Set("If-None-Match", src.ETag)
	}
	if src.LastModified != "" {
		req.Header.Set("If-Modified-Since", src.LastModified)
	}

	resp, err := w.httpClient().Do(req)
	now := time.Now().UTC()
	if err != nil {
		_ = w.Store.MarkM3UStatus(ctx, id, "error: "+err.Error(), src.ETag, src.LastModified, now)
		return fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified {
		if err := w.Store.MarkM3UStatus(ctx, id, "ok", src.ETag, src.LastModified, now); err != nil {
			return fmt.Errorf("mark status: %w", err)
		}
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		status := fmt.Sprintf("error: HTTP %d", resp.StatusCode)
		_ = w.Store.MarkM3UStatus(ctx, id, status, src.ETag, src.LastModified, now)
		return fmt.Errorf("http %d", resp.StatusCode)
	}

	entries, parseErr := m3u.Parse(resp.Body)
	if parseErr != nil {
		_ = w.Store.MarkM3UStatus(ctx, id, "error: "+parseErr.Error(), src.ETag, src.LastModified, now)
		return fmt.Errorf("parse m3u: %w", parseErr)
	}

	seen := make([]string, 0, len(entries))
	for _, e := range entries {
		sourceChannelID := e.TvgID
		if sourceChannelID == "" {
			sourceChannelID = "name:" + slug(e.Title) + ":" + sha8(e.URL)
		}
		ch := store.Channel{
			SourceM3UID:      id,
			SourceChannelID:  sourceChannelID,
			DisplayName:      e.Title,
			LogoURL:          e.TvgLogo,
			UpstreamURL:      e.URL,
			ChannelNumberSrc: e.TvgChno,
			GroupTitleSrc:    e.GroupTitle,
			Attrs:            e.Attrs,
		}
		if _, err := w.Store.UpsertChannelFromM3U(ctx, ch); err != nil {
			_ = w.Store.MarkM3UStatus(ctx, id, "error: "+err.Error(), src.ETag, src.LastModified, now)
			return fmt.Errorf("upsert channel: %w", err)
		}
		seen = append(seen, sourceChannelID)
	}
	if err := w.Store.MarkChannelsMissing(ctx, id, seen); err != nil {
		_ = w.Store.MarkM3UStatus(ctx, id, "error: "+err.Error(), src.ETag, src.LastModified, now)
		return fmt.Errorf("mark missing: %w", err)
	}

	// Drain any body remnants so the connection can be reused.
	_, _ = io.Copy(io.Discard, resp.Body)

	etag := resp.Header.Get("ETag")
	lastModified := resp.Header.Get("Last-Modified")
	if err := w.Store.MarkM3UStatus(ctx, id, "ok", etag, lastModified, now); err != nil {
		return fmt.Errorf("mark status: %w", err)
	}
	return nil
}

// slugRegex matches runs of any character that are NOT in [a-z0-9]. We collapse
// those runs to a single '-' when building a fallback source_channel_id so the
// resulting identifier is URL-safe and stable across refreshes.
var slugRegex = regexp.MustCompile(`[^a-z0-9]+`)

// slug lowercases s and replaces any run of non-[a-z0-9] characters with '-'.
// Leading and trailing dashes are trimmed so consecutive separators at the
// boundaries don't leak into the final identifier.
func slug(s string) string {
	s = strings.ToLower(s)
	s = slugRegex.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

// sha8 returns the first 8 hex characters of sha256(s). Used together with the
// title slug to derive a deterministic fallback identifier when an upstream
// entry has no tvg-id.
func sha8(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:8]
}
