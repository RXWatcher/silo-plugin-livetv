package refresh_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/go-hclog"

	"github.com/ContinuumApp/continuum-plugin-livetv/internal/refresh"
	"github.com/ContinuumApp/continuum-plugin-livetv/internal/store"
	"github.com/ContinuumApp/continuum-plugin-livetv/internal/testutil"
)

// seedM3USource creates a parent m3u_sources row pointing at url. We surface
// the helper here (separate from the store-package version) because tests in
// this package can't import store_test helpers.
func seedM3USource(t *testing.T, ctx context.Context, s *store.Store, url string) string {
	t.Helper()
	src, err := s.CreateM3USource(ctx, store.M3USource{
		Name: "test-src", URL: url, Enabled: true, RefreshInterval: time.Hour,
	})
	if err != nil {
		t.Fatalf("seed m3u: %v", err)
	}
	return src.ID
}

// channelByKey looks up a channel by (source, source_channel_id) directly via
// the pool so tests don't depend on a list/scan helper.
func channelByKey(t *testing.T, ctx context.Context, s *store.Store, sourceID, key string) (string, bool) {
	t.Helper()
	var id string
	var enabled bool
	err := s.Pool.QueryRow(ctx, `
		SELECT id, enabled_src FROM channels
		WHERE source_m3u_id = $1 AND source_channel_id = $2
	`, sourceID, key).Scan(&id, &enabled)
	if err != nil {
		return "", false
	}
	_ = enabled
	return id, true
}

func enabledSrc(t *testing.T, ctx context.Context, s *store.Store, channelID string) bool {
	t.Helper()
	var enabled bool
	if err := s.Pool.QueryRow(ctx,
		`SELECT enabled_src FROM channels WHERE id = $1`, channelID).Scan(&enabled); err != nil {
		t.Fatalf("read enabled_src: %v", err)
	}
	return enabled
}

func TestRefreshOne_HappyPath(t *testing.T) {
	pool := testutil.StartPG(t)
	s := store.New(pool)
	ctx := context.Background()

	const body = "#EXTM3U\n" +
		"#EXTINF:-1 tvg-id=\"a\" group-title=\"G\",A\n" +
		"http://up/a.ts\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Last-Modified", "Mon, 02 Jan 2006 15:04:05 GMT")
		_, _ = fmt.Fprint(w, body)
	}))
	defer srv.Close()

	id := seedM3USource(t, ctx, s, srv.URL)
	w := &refresh.M3UWorker{Store: s, Logger: hclog.NewNullLogger()}

	if err := w.RefreshOne(ctx, id); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	chID, ok := channelByKey(t, ctx, s, id, "a")
	if !ok {
		t.Fatal("channel not inserted")
	}
	if !enabledSrc(t, ctx, s, chID) {
		t.Fatal("expected enabled_src=true")
	}

	src, err := s.GetM3USource(ctx, id)
	if err != nil {
		t.Fatalf("get source: %v", err)
	}
	if src.ETag != `"v1"` {
		t.Errorf("etag = %q, want \"v1\"", src.ETag)
	}
	if src.LastStatus != "ok" {
		t.Errorf("status = %q, want ok", src.LastStatus)
	}
}

func TestRefreshOne_304_NoMutation(t *testing.T) {
	pool := testutil.StartPG(t)
	s := store.New(pool)
	ctx := context.Background()

	// First serve real content with an ETag, then 304 on the conditional GET.
	calls := 0
	const body = "#EXTM3U\n" +
		"#EXTINF:-1 tvg-id=\"a\",A\n" +
		"http://up/a.ts\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("ETag", `"v1"`)
			_, _ = fmt.Fprint(w, body)
			return
		}
		if r.Header.Get("If-None-Match") != `"v1"` {
			t.Errorf("missing if-none-match on call %d", calls)
		}
		w.WriteHeader(http.StatusNotModified)
	}))
	defer srv.Close()

	id := seedM3USource(t, ctx, s, srv.URL)
	w := &refresh.M3UWorker{Store: s, Logger: hclog.NewNullLogger()}
	if err := w.RefreshOne(ctx, id); err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	chID, ok := channelByKey(t, ctx, s, id, "a")
	if !ok {
		t.Fatal("channel missing after first refresh")
	}

	// Capture updated_at before the 304 call so we can prove channel rows
	// were untouched.
	var beforeUpdated time.Time
	if err := s.Pool.QueryRow(ctx,
		`SELECT updated_at FROM channels WHERE id = $1`, chID).Scan(&beforeUpdated); err != nil {
		t.Fatalf("read updated_at: %v", err)
	}

	if err := w.RefreshOne(ctx, id); err != nil {
		t.Fatalf("second refresh: %v", err)
	}

	var afterUpdated time.Time
	if err := s.Pool.QueryRow(ctx,
		`SELECT updated_at FROM channels WHERE id = $1`, chID).Scan(&afterUpdated); err != nil {
		t.Fatalf("read updated_at: %v", err)
	}
	if !afterUpdated.Equal(beforeUpdated) {
		t.Errorf("channel updated_at changed on 304: %v -> %v", beforeUpdated, afterUpdated)
	}

	src, err := s.GetM3USource(ctx, id)
	if err != nil {
		t.Fatalf("get source: %v", err)
	}
	if src.LastStatus != "ok" {
		t.Errorf("status = %q, want ok", src.LastStatus)
	}
	if src.LastRefreshedAt == nil {
		t.Error("last_refreshed_at should be populated after 304")
	}
}

func TestRefreshOne_SoftDisablesMissingChannels(t *testing.T) {
	pool := testutil.StartPG(t)
	s := store.New(pool)
	ctx := context.Background()

	bodies := []string{
		"#EXTM3U\n" +
			"#EXTINF:-1 tvg-id=\"a\",A\nhttp://up/a.ts\n" +
			"#EXTINF:-1 tvg-id=\"b\",B\nhttp://up/b.ts\n",
		"#EXTM3U\n" +
			"#EXTINF:-1 tvg-id=\"a\",A\nhttp://up/a.ts\n",
	}
	call := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Distinct ETags ensure neither call short-circuits as 304.
		w.Header().Set("ETag", fmt.Sprintf(`"v%d"`, call+1))
		_, _ = fmt.Fprint(w, bodies[call])
		call++
	}))
	defer srv.Close()

	id := seedM3USource(t, ctx, s, srv.URL)
	w := &refresh.M3UWorker{Store: s, Logger: hclog.NewNullLogger()}
	if err := w.RefreshOne(ctx, id); err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	if err := w.RefreshOne(ctx, id); err != nil {
		t.Fatalf("second refresh: %v", err)
	}

	aID, ok := channelByKey(t, ctx, s, id, "a")
	if !ok {
		t.Fatal("A missing after second refresh")
	}
	if !enabledSrc(t, ctx, s, aID) {
		t.Error("A should remain enabled")
	}
	bID, ok := channelByKey(t, ctx, s, id, "b")
	if !ok {
		t.Fatal("B should still exist (soft-disabled, not deleted)")
	}
	if enabledSrc(t, ctx, s, bID) {
		t.Error("B should be soft-disabled")
	}
}

func TestRefreshOne_HTTPError(t *testing.T) {
	pool := testutil.StartPG(t)
	s := store.New(pool)
	ctx := context.Background()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	id := seedM3USource(t, ctx, s, srv.URL)
	w := &refresh.M3UWorker{Store: s, Logger: hclog.NewNullLogger()}
	if err := w.RefreshOne(ctx, id); err == nil {
		t.Fatal("expected error for HTTP 500")
	}

	src, err := s.GetM3USource(ctx, id)
	if err != nil {
		t.Fatalf("get source: %v", err)
	}
	if !strings.HasPrefix(src.LastStatus, "error: HTTP 500") {
		t.Errorf("status = %q, want prefix 'error: HTTP 500'", src.LastStatus)
	}

	// No channel rows should exist.
	var n int
	if err := s.Pool.QueryRow(ctx,
		`SELECT count(*) FROM channels WHERE source_m3u_id = $1`, id).Scan(&n); err != nil {
		t.Fatalf("count channels: %v", err)
	}
	if n != 0 {
		t.Errorf("got %d channels, want 0", n)
	}
}

func TestRefreshOne_FallbackSourceChannelIDWhenNoTvgID(t *testing.T) {
	pool := testutil.StartPG(t)
	s := store.New(pool)
	ctx := context.Background()

	// No tvg-id attribute. The worker should fall back to "name:<slug>:<sha8(url)>".
	const body = "#EXTM3U\n" +
		"#EXTINF:-1 group-title=\"News\",My Channel!\n" +
		"http://up/stream.ts\n"
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("ETag", fmt.Sprintf(`"v%d"`, calls))
		_, _ = fmt.Fprint(w, body)
	}))
	defer srv.Close()

	id := seedM3USource(t, ctx, s, srv.URL)
	w := &refresh.M3UWorker{Store: s, Logger: hclog.NewNullLogger()}

	if err := w.RefreshOne(ctx, id); err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	// Look up any single channel for this source and capture its id.
	var firstID, srcChanID string
	if err := s.Pool.QueryRow(ctx,
		`SELECT id, source_channel_id FROM channels WHERE source_m3u_id = $1`, id).
		Scan(&firstID, &srcChanID); err != nil {
		t.Fatalf("read channel: %v", err)
	}
	if !strings.HasPrefix(srcChanID, "name:") {
		t.Errorf("source_channel_id = %q, want prefix 'name:'", srcChanID)
	}

	// A second refresh of the same playlist must reuse the same row id (deterministic).
	if err := w.RefreshOne(ctx, id); err != nil {
		t.Fatalf("second refresh: %v", err)
	}
	var secondID string
	if err := s.Pool.QueryRow(ctx,
		`SELECT id FROM channels WHERE source_m3u_id = $1`, id).Scan(&secondID); err != nil {
		t.Fatalf("read channel post-second-refresh: %v", err)
	}
	if firstID != secondID {
		t.Errorf("row id changed: %s -> %s (fallback id was not stable)", firstID, secondID)
	}
}
