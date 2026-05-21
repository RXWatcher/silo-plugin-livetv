package refresh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/hashicorp/go-hclog"

	"github.com/RXWatcher/continuum-plugin-livetv/internal/store"
	"github.com/RXWatcher/continuum-plugin-livetv/internal/xmltv"
)

// pruneAge is the cutoff applied after every successful XMLTV refresh: any
// programme whose stop_utc precedes (now - pruneAge) is deleted. The 6-hour
// grace window keeps the "what was on earlier today" lookups working while
// preventing the table from accumulating indefinitely.
const pruneAge = 6 * time.Hour

// XMLTVWorker pulls XMLTV documents from configured upstreams, streams them
// into the store, and auto-links channels by source_channel_id. Mirrors the
// shape of M3UWorker so the scheduler can dispatch them uniformly.
type XMLTVWorker struct {
	Store  *store.Store
	Client *http.Client
	Logger hclog.Logger
}

// httpClient returns the worker's HTTP client, defaulting to
// http.DefaultClient when none was supplied.
func (w *XMLTVWorker) httpClient() *http.Client {
	if w.Client != nil {
		return w.Client
	}
	return http.DefaultClient
}

// logger returns the worker's logger, defaulting to a null logger.
func (w *XMLTVWorker) logger() hclog.Logger {
	if w.Logger != nil {
		return w.Logger
	}
	return hclog.NewNullLogger()
}

// RefreshAll iterates every enabled XMLTV source. Failures on individual
// sources are logged but do not abort siblings; errors are joined and
// returned to the caller.
func (w *XMLTVWorker) RefreshAll(ctx context.Context) error {
	srcs, err := w.Store.ListXMLTVSources(ctx)
	if err != nil {
		return fmt.Errorf("list xmltv sources: %w", err)
	}
	var errs []error
	for _, src := range srcs {
		if !src.Enabled {
			continue
		}
		if err := w.RefreshOne(ctx, src.ID); err != nil {
			w.logger().Warn("xmltv refresh failed", "source", src.ID, "name", src.Name, "err", err)
			errs = append(errs, fmt.Errorf("source %s: %w", src.ID, err))
		}
	}
	return errors.Join(errs...)
}

// RefreshOne refreshes a single XMLTV source by id. ParseAuto transparently
// handles gzip-encoded bodies. On success the future programs for each
// xmltv channel id are replaced atomically, auto-link rows are inserted for
// channels whose source_channel_id matches the xmltv channel id, and
// programs older than pruneAge are pruned.
func (w *XMLTVWorker) RefreshOne(ctx context.Context, id string) error {
	src, err := w.Store.GetXMLTVSource(ctx, id)
	if err != nil {
		return fmt.Errorf("get xmltv source: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src.URL, nil)
	if err != nil {
		now := time.Now().UTC()
		_ = w.Store.MarkXMLTVStatus(ctx, id, "error: "+err.Error(), src.ETag, src.LastModified, now)
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
		_ = w.Store.MarkXMLTVStatus(ctx, id, "error: "+err.Error(), src.ETag, src.LastModified, now)
		return fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified {
		if err := w.Store.MarkXMLTVStatus(ctx, id, "ok", src.ETag, src.LastModified, now); err != nil {
			return fmt.Errorf("mark status: %w", err)
		}
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		status := fmt.Sprintf("error: HTTP %d", resp.StatusCode)
		_ = w.Store.MarkXMLTVStatus(ctx, id, status, src.ETag, src.LastModified, now)
		return fmt.Errorf("http %d", resp.StatusCode)
	}

	var (
		channelIDs    []string
		programsByCh  = make(map[string][]store.Program)
	)
	onChannel := func(c xmltv.Channel) error {
		channelIDs = append(channelIDs, c.ID)
		return nil
	}
	onProgramme := func(p xmltv.Programme) error {
		programsByCh[p.Channel] = append(programsByCh[p.Channel], toStoreProgram(p))
		return nil
	}

	if parseErr := xmltv.ParseAuto(resp.Body, onChannel, onProgramme); parseErr != nil {
		_ = w.Store.MarkXMLTVStatus(ctx, id, "error: "+parseErr.Error(), src.ETag, src.LastModified, now)
		return fmt.Errorf("parse xmltv: %w", parseErr)
	}

	for chID, progs := range programsByCh {
		if err := w.Store.ReplaceFutureForChannel(ctx, chID, progs); err != nil {
			_ = w.Store.MarkXMLTVStatus(ctx, id, "error: "+err.Error(), src.ETag, src.LastModified, now)
			return fmt.Errorf("replace future %s: %w", chID, err)
		}
	}

	for _, chID := range channelIDs {
		if err := w.autoLinkEPG(ctx, chID); err != nil {
			_ = w.Store.MarkXMLTVStatus(ctx, id, "error: "+err.Error(), src.ETag, src.LastModified, now)
			return fmt.Errorf("auto-link %s: %w", chID, err)
		}
	}

	if _, err := w.Store.PruneOldPrograms(ctx, time.Now().Add(-pruneAge)); err != nil {
		_ = w.Store.MarkXMLTVStatus(ctx, id, "error: "+err.Error(), src.ETag, src.LastModified, now)
		return fmt.Errorf("prune: %w", err)
	}

	// Drain body remnants so the underlying connection can be reused.
	_, _ = io.Copy(io.Discard, resp.Body)

	etag := resp.Header.Get("ETag")
	lastModified := resp.Header.Get("Last-Modified")
	if err := w.Store.MarkXMLTVStatus(ctx, id, "ok", etag, lastModified, now); err != nil {
		return fmt.Errorf("mark status: %w", err)
	}
	return nil
}

// autoLinkEPG inserts auto-link rows for every channel whose
// source_channel_id matches xmltvChannelID. Channels already linked stay
// linked thanks to ON CONFLICT DO NOTHING; manual links (auto_linked=false)
// are preserved untouched.
//
// Inlined here (rather than added to the store) so we don't extend the
// settled Phase 3 store contract.
func (w *XMLTVWorker) autoLinkEPG(ctx context.Context, xmltvChannelID string) error {
	_, err := w.Store.Pool.Exec(ctx, `
		INSERT INTO channel_epg_keys (channel_id, xmltv_channel_id, auto_linked)
		SELECT c.id, $1, true
		FROM channels c
		WHERE c.source_channel_id = $1
		ON CONFLICT (channel_id, xmltv_channel_id) DO NOTHING
	`, xmltvChannelID)
	if err != nil {
		return fmt.Errorf("insert channel_epg_keys: %w", err)
	}
	return nil
}

// toStoreProgram converts an xmltv.Programme value into the store.Program
// representation expected by ReplaceFutureForChannel.
func toStoreProgram(p xmltv.Programme) store.Program {
	out := store.Program{
		XMLTVChannelID:  p.Channel,
		Title:           p.Title,
		SubTitle:        p.SubTitle,
		Description:     p.Description,
		Categories:      p.Categories,
		EpisodeNum:      p.EpisodeNum,
		SeasonNum:       p.SeasonNum,
		Episode:         p.Episode,
		Rating:          p.Rating,
		IconURL:         p.IconURL,
		Start:           p.Start,
		Stop:            p.Stop,
		OriginalAirDate: p.OriginalAirDate,
	}
	if len(p.Credits) > 0 {
		out.Credits = make([]store.Credit, 0, len(p.Credits))
		for _, c := range p.Credits {
			out.Credits = append(out.Credits, store.Credit{
				Kind:     c.Kind,
				Name:     c.Name,
				Position: c.Pos,
			})
		}
	}
	return out
}
