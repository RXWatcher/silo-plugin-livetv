package refresh_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hashicorp/go-hclog"

	"github.com/RXWatcher/continuum-plugin-livetv/internal/refresh"
	"github.com/RXWatcher/continuum-plugin-livetv/internal/store"
	"github.com/RXWatcher/continuum-plugin-livetv/internal/testutil"
)

// seedXMLTVSource creates a parent xmltv_sources row pointing at url. Mirrors
// seedM3USource from m3u_test.go.
func seedXMLTVSource(t *testing.T, ctx context.Context, s *store.Store, url string) string {
	t.Helper()
	src, err := s.CreateXMLTVSource(ctx, store.XMLTVSource{
		Name: "test-xmltv", URL: url, Enabled: true, RefreshInterval: time.Hour,
	})
	if err != nil {
		t.Fatalf("seed xmltv: %v", err)
	}
	return src.ID
}

// seedChannel creates a channels row with the supplied source_channel_id so
// auto-link assertions have something to match against.
func seedChannel(t *testing.T, ctx context.Context, s *store.Store, srcM3UID, srcChID, name string) string {
	t.Helper()
	id, err := s.UpsertChannelFromM3U(ctx, store.Channel{
		SourceM3UID:     srcM3UID,
		SourceChannelID: srcChID,
		DisplayName:     name,
		UpstreamURL:     "http://up/" + srcChID,
	})
	if err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	return id
}

// buildXMLTV returns a small XMLTV document with the supplied channel id and
// programme start/stop times (UTC) so tests can pin "future" placement.
func buildXMLTV(chID string, programmes []xmltvProg) string {
	var buf bytes.Buffer
	buf.WriteString(`<?xml version="1.0"?><tv>`)
	fmt.Fprintf(&buf, `<channel id="%s"><display-name>%s</display-name></channel>`, chID, chID)
	for _, p := range programmes {
		fmt.Fprintf(&buf,
			`<programme channel="%s" start="%s" stop="%s"><title>%s</title></programme>`,
			p.channel,
			p.start.UTC().Format("20060102150405 -0700"),
			p.stop.UTC().Format("20060102150405 -0700"),
			p.title,
		)
	}
	buf.WriteString(`</tv>`)
	return buf.String()
}

type xmltvProg struct {
	channel string
	start   time.Time
	stop    time.Time
	title   string
}

func TestRefreshOneXMLTV_HappyPath(t *testing.T) {
	pool := testutil.StartPG(t)
	s := store.New(pool)
	ctx := context.Background()

	m3uID := seedM3USource(t, ctx, s, "http://m3u")
	chID := seedChannel(t, ctx, s, m3uID, "chan1", "Channel One")

	start := time.Now().Add(time.Hour).Truncate(time.Second)
	stop := start.Add(time.Hour)
	body := buildXMLTV("chan1", []xmltvProg{{channel: "chan1", start: start, stop: stop, title: "Show A"}})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		_, _ = fmt.Fprint(w, body)
	}))
	defer srv.Close()

	id := seedXMLTVSource(t, ctx, s, srv.URL)
	w := &refresh.XMLTVWorker{Store: s, Logger: hclog.NewNullLogger()}
	if err := w.RefreshOne(ctx, id); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	// Program persisted?
	var n int
	if err := s.Pool.QueryRow(ctx,
		`SELECT count(*) FROM programs WHERE xmltv_channel_id = $1`, "chan1").Scan(&n); err != nil {
		t.Fatalf("count programs: %v", err)
	}
	if n != 1 {
		t.Errorf("got %d programs, want 1", n)
	}

	// Auto-link row created?
	keys, err := s.ListEPGKeys(ctx, chID)
	if err != nil {
		t.Fatalf("list epg keys: %v", err)
	}
	if len(keys) != 1 || keys[0] != "chan1" {
		t.Errorf("epg keys = %v, want [chan1]", keys)
	}

	src, err := s.GetXMLTVSource(ctx, id)
	if err != nil {
		t.Fatalf("get source: %v", err)
	}
	if src.LastStatus != "ok" {
		t.Errorf("status = %q, want ok", src.LastStatus)
	}
}

func TestRefreshOneXMLTV_HandlesGzip(t *testing.T) {
	pool := testutil.StartPG(t)
	s := store.New(pool)
	ctx := context.Background()

	m3uID := seedM3USource(t, ctx, s, "http://m3u")
	chID := seedChannel(t, ctx, s, m3uID, "gz1", "GZ One")

	start := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	stop := start.Add(time.Hour)
	xml := buildXMLTV("gz1", []xmltvProg{{channel: "gz1", start: start, stop: stop, title: "Gzipped"}})

	var gzBuf bytes.Buffer
	gw := gzip.NewWriter(&gzBuf)
	if _, err := gw.Write([]byte(xml)); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	gzBytes := gzBuf.Bytes()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("ETag", `"gz1"`)
		_, _ = w.Write(gzBytes)
	}))
	defer srv.Close()

	id := seedXMLTVSource(t, ctx, s, srv.URL)
	w := &refresh.XMLTVWorker{Store: s, Logger: hclog.NewNullLogger()}
	if err := w.RefreshOne(ctx, id); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	var n int
	if err := s.Pool.QueryRow(ctx,
		`SELECT count(*) FROM programs WHERE xmltv_channel_id = $1`, "gz1").Scan(&n); err != nil {
		t.Fatalf("count programs: %v", err)
	}
	if n != 1 {
		t.Errorf("got %d programs, want 1", n)
	}
	keys, err := s.ListEPGKeys(ctx, chID)
	if err != nil {
		t.Fatalf("list epg keys: %v", err)
	}
	if len(keys) != 1 {
		t.Errorf("auto-link missing, keys=%v", keys)
	}
}

func TestRefreshOneXMLTV_ReplaceFutureClearsStale(t *testing.T) {
	pool := testutil.StartPG(t)
	s := store.New(pool)
	ctx := context.Background()

	m3uID := seedM3USource(t, ctx, s, "http://m3u")
	_ = seedChannel(t, ctx, s, m3uID, "rep1", "Replace One")
	_ = seedChannel(t, ctx, s, m3uID, "other", "Other One")

	now := time.Now().Truncate(time.Second)
	first := buildXMLTV("rep1", []xmltvProg{
		{channel: "rep1", start: now.Add(1 * time.Hour), stop: now.Add(2 * time.Hour), title: "A"},
		{channel: "rep1", start: now.Add(2 * time.Hour), stop: now.Add(3 * time.Hour), title: "B"},
		{channel: "rep1", start: now.Add(3 * time.Hour), stop: now.Add(4 * time.Hour), title: "C"},
	})
	second := buildXMLTV("rep1", []xmltvProg{
		{channel: "rep1", start: now.Add(5 * time.Hour), stop: now.Add(6 * time.Hour), title: "X"},
		{channel: "rep1", start: now.Add(6 * time.Hour), stop: now.Add(7 * time.Hour), title: "Y"},
	})

	// Pre-seed an "other" channel program so we can verify untouched cross-channel rows.
	otherStart := now.Add(2 * time.Hour)
	other := buildXMLTV("other", []xmltvProg{
		{channel: "other", start: otherStart, stop: otherStart.Add(time.Hour), title: "Untouched"},
	})

	bodies := []string{first, other, second}
	idx := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("ETag", fmt.Sprintf(`"v%d"`, idx+1))
		_, _ = fmt.Fprint(w, bodies[idx])
		idx++
	}))
	defer srv.Close()

	srcID := seedXMLTVSource(t, ctx, s, srv.URL)
	w := &refresh.XMLTVWorker{Store: s, Logger: hclog.NewNullLogger()}

	// Run 1: rep1 gets A,B,C.
	if err := w.RefreshOne(ctx, srcID); err != nil {
		t.Fatalf("refresh 1: %v", err)
	}
	// Run 2: "other" channel gets its programme; rep1 not in this doc.
	if err := w.RefreshOne(ctx, srcID); err != nil {
		t.Fatalf("refresh 2: %v", err)
	}
	// Run 3: rep1 gets X,Y — replaces A,B,C.
	if err := w.RefreshOne(ctx, srcID); err != nil {
		t.Fatalf("refresh 3: %v", err)
	}

	var n int
	if err := s.Pool.QueryRow(ctx,
		`SELECT count(*) FROM programs WHERE xmltv_channel_id = $1`, "rep1").Scan(&n); err != nil {
		t.Fatalf("count rep1: %v", err)
	}
	if n != 2 {
		t.Errorf("rep1 programs = %d, want 2", n)
	}

	if err := s.Pool.QueryRow(ctx,
		`SELECT count(*) FROM programs WHERE xmltv_channel_id = $1`, "other").Scan(&n); err != nil {
		t.Fatalf("count other: %v", err)
	}
	if n != 1 {
		t.Errorf("other programs = %d, want 1", n)
	}
}

func TestRefreshOneXMLTV_PruneRemovesAncient(t *testing.T) {
	pool := testutil.StartPG(t)
	s := store.New(pool)
	ctx := context.Background()

	m3uID := seedM3USource(t, ctx, s, "http://m3u")
	_ = seedChannel(t, ctx, s, m3uID, "fresh", "Fresh")

	// Seed an ancient program for an unrelated channel directly via the pool.
	old := time.Now().Add(-7 * 24 * time.Hour)
	if _, err := s.Pool.Exec(ctx, `
		INSERT INTO programs (id, xmltv_channel_id, start_utc, stop_utc, title, sub_title,
			description, episode_num, season_num, episode, categories, rating, icon_url, original_air_date)
		VALUES ($1,$2,$3,$4,$5,'','','',NULL,NULL,$6,'','',NULL)
	`, "ancient-id", "ancient", old, old.Add(time.Hour), "Ancient", []string{}); err != nil {
		t.Fatalf("seed ancient: %v", err)
	}

	// Now run a refresh on a different channel — prune should still run.
	startFresh := time.Now().Add(time.Hour).Truncate(time.Second)
	body := buildXMLTV("fresh", []xmltvProg{
		{channel: "fresh", start: startFresh, stop: startFresh.Add(time.Hour), title: "Now"},
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, body)
	}))
	defer srv.Close()
	srcID := seedXMLTVSource(t, ctx, s, srv.URL)
	w := &refresh.XMLTVWorker{Store: s, Logger: hclog.NewNullLogger()}
	if err := w.RefreshOne(ctx, srcID); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	var n int
	if err := s.Pool.QueryRow(ctx,
		`SELECT count(*) FROM programs WHERE id = $1`, "ancient-id").Scan(&n); err != nil {
		t.Fatalf("count ancient: %v", err)
	}
	if n != 0 {
		t.Errorf("ancient survived prune: count=%d", n)
	}
}

func TestRefreshOneXMLTV_304(t *testing.T) {
	pool := testutil.StartPG(t)
	s := store.New(pool)
	ctx := context.Background()

	m3uID := seedM3USource(t, ctx, s, "http://m3u")
	_ = seedChannel(t, ctx, s, m3uID, "v1ch", "V1")

	startFuture := time.Now().Add(time.Hour).Truncate(time.Second)
	body := buildXMLTV("v1ch", []xmltvProg{
		{channel: "v1ch", start: startFuture, stop: startFuture.Add(time.Hour), title: "X"},
	})

	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("ETag", `"v1"`)
			w.Header().Set("Last-Modified", "Mon, 02 Jan 2006 15:04:05 GMT")
			_, _ = fmt.Fprint(w, body)
			return
		}
		if r.Header.Get("If-None-Match") != `"v1"` {
			t.Errorf("missing If-None-Match")
		}
		if r.Header.Get("If-Modified-Since") != "Mon, 02 Jan 2006 15:04:05 GMT" {
			t.Errorf("missing If-Modified-Since")
		}
		w.WriteHeader(http.StatusNotModified)
	}))
	defer srv.Close()
	id := seedXMLTVSource(t, ctx, s, srv.URL)

	w := &refresh.XMLTVWorker{Store: s, Logger: hclog.NewNullLogger()}
	if err := w.RefreshOne(ctx, id); err != nil {
		t.Fatalf("first: %v", err)
	}

	// Capture programs.updated_at-equivalent — programs has no updated_at;
	// instead snapshot the row count and ids.
	var beforeIDs []string
	rows, err := s.Pool.Query(ctx,
		`SELECT id FROM programs WHERE xmltv_channel_id = $1 ORDER BY id`, "v1ch")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	for rows.Next() {
		var pid string
		if err := rows.Scan(&pid); err != nil {
			t.Fatalf("scan: %v", err)
		}
		beforeIDs = append(beforeIDs, pid)
	}
	rows.Close()

	if err := w.RefreshOne(ctx, id); err != nil {
		t.Fatalf("second (304): %v", err)
	}

	var afterIDs []string
	rows, err = s.Pool.Query(ctx,
		`SELECT id FROM programs WHERE xmltv_channel_id = $1 ORDER BY id`, "v1ch")
	if err != nil {
		t.Fatalf("snapshot 2: %v", err)
	}
	for rows.Next() {
		var pid string
		if err := rows.Scan(&pid); err != nil {
			t.Fatalf("scan: %v", err)
		}
		afterIDs = append(afterIDs, pid)
	}
	rows.Close()

	if fmt.Sprint(beforeIDs) != fmt.Sprint(afterIDs) {
		t.Errorf("programs changed on 304: before=%v after=%v", beforeIDs, afterIDs)
	}

	src, err := s.GetXMLTVSource(ctx, id)
	if err != nil {
		t.Fatalf("get source: %v", err)
	}
	if src.ETag != `"v1"` {
		t.Errorf("etag changed: %q", src.ETag)
	}
	if src.LastStatus != "ok" {
		t.Errorf("status = %q, want ok", src.LastStatus)
	}
}
