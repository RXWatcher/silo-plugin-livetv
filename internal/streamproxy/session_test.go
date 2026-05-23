package streamproxy_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/hashicorp/go-hclog"

	"github.com/RXWatcher/silo-plugin-livetv/internal/store"
	"github.com/RXWatcher/silo-plugin-livetv/internal/streamproxy"
	"github.com/RXWatcher/silo-plugin-livetv/internal/testutil"
)

// seedM3USource creates a parent m3u_sources row pointing at url so channel
// inserts pass the FK check. Mirrors the helper used in store + refresh tests.
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

// seedChannel inserts a single channel under a fresh source and returns the
// channel id together with the parent source id. The upstream URL is overrideable
// so tests can point at their own httptest servers.
func seedChannel(t *testing.T, ctx context.Context, s *store.Store, sourceID, name, upstream string) string {
	t.Helper()
	id, err := s.UpsertChannelFromM3U(ctx, store.Channel{
		SourceM3UID:     sourceID,
		SourceChannelID: name,
		DisplayName:     name,
		UpstreamURL:     upstream,
		UpstreamKind:    "mpegts",
	})
	if err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	return id
}

// newRouter builds a chi router with the live route mounted so chi.URLParam
// resolves correctly inside the handler. Returns the router + a helper that
// runs a request through it with the userID context already attached.
func newRouter(deps *streamproxy.Deps) *chi.Mux {
	r := chi.NewRouter()
	r.Post("/api/v1/livetv/channels/{id}/stream", deps.CreateSession)
	return r
}

// runRequest issues a request against r with the given userID attached.
func runRequest(t *testing.T, r http.Handler, userID string, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	if userID != "" {
		req = req.WithContext(streamproxy.WithUserID(req.Context(), userID))
	}
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	return rr
}

func TestCreateSession_HappyPath(t *testing.T) {
	pool := testutil.StartPG(t)
	s := store.New(pool)
	ctx := context.Background()

	src := seedM3USource(t, ctx, s, "http://example.invalid/playlist.m3u8")
	chID := seedChannel(t, ctx, s, src, "ch1", "http://example.invalid/ch1.ts")

	deps := &streamproxy.Deps{
		Store:    s,
		Settings: streamproxy.StaticSettings{PerUser: 3, PerChannel: 5, IdleTimeout: 60 * time.Second},
		Logger:   hclog.NewNullLogger(),
	}
	r := newRouter(deps)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/livetv/channels/"+chID+"/stream", nil)
	rr := runRequest(t, r, "user-1", req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}

	// Cookie set with the session-token shape and the proxy's stream Path.
	var sessCookie *http.Cookie
	for _, c := range rr.Result().Cookies() {
		if c.Name == "livetv_stream" {
			sessCookie = c
			break
		}
	}
	if sessCookie == nil {
		t.Fatal("livetv_stream cookie not set")
	}
	if !strings.Contains(sessCookie.Value, ".") {
		t.Errorf("cookie value %q missing '.' separator", sessCookie.Value)
	}
	if sessCookie.Path != "/api/v1/livetv/stream/" {
		t.Errorf("cookie Path = %q", sessCookie.Path)
	}

	var body struct {
		SessionID   string    `json:"session_id"`
		PlaybackURL string    `json:"playback_url"`
		ExpiresAt   time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.SessionID == "" {
		t.Error("session_id empty")
	}
	wantURL := "/api/v1/livetv/stream/" + body.SessionID + ".ts"
	if body.PlaybackURL != wantURL {
		t.Errorf("playback_url = %q, want %q", body.PlaybackURL, wantURL)
	}
	if time.Until(body.ExpiresAt) < 7*time.Hour {
		t.Errorf("expires_at too soon: %v", body.ExpiresAt)
	}

	// Session row exists with the expected secret length.
	sess, err := s.GetSession(ctx, body.SessionID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if len(sess.SessionSecret) != 32 {
		t.Errorf("secret len = %d, want 32", len(sess.SessionSecret))
	}
	if sess.ScopedGrantID != sess.ID {
		t.Errorf("scoped_grant_id = %q, want %q", sess.ScopedGrantID, sess.ID)
	}

	// MarkTuned ran.
	recents, err := s.ListRecent(ctx, "user-1", 0)
	if err != nil {
		t.Fatalf("list recent: %v", err)
	}
	if len(recents) != 1 || recents[0].ChannelID != chID {
		t.Errorf("recents = %+v, want one row for %s", recents, chID)
	}
}

func TestCreateSession_Unauthenticated(t *testing.T) {
	pool := testutil.StartPG(t)
	s := store.New(pool)
	ctx := context.Background()
	src := seedM3USource(t, ctx, s, "http://x")
	chID := seedChannel(t, ctx, s, src, "c", "http://x/c.ts")

	deps := &streamproxy.Deps{
		Store:    s,
		Settings: streamproxy.StaticSettings{PerUser: 3, PerChannel: 5},
		Logger:   hclog.NewNullLogger(),
	}
	r := newRouter(deps)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/livetv/channels/"+chID+"/stream", nil)
	rr := runRequest(t, r, "" /* no user */, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rr.Code)
	}
}

func TestCreateSession_UnknownChannel(t *testing.T) {
	pool := testutil.StartPG(t)
	s := store.New(pool)
	deps := &streamproxy.Deps{
		Store:    s,
		Settings: streamproxy.StaticSettings{PerUser: 3, PerChannel: 5},
		Logger:   hclog.NewNullLogger(),
	}
	r := newRouter(deps)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/livetv/channels/doesnotexist/stream", nil)
	rr := runRequest(t, r, "u", req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rr.Code)
	}
}

func TestCreateSession_DisabledChannel(t *testing.T) {
	pool := testutil.StartPG(t)
	s := store.New(pool)
	ctx := context.Background()
	src := seedM3USource(t, ctx, s, "http://x")
	chID := seedChannel(t, ctx, s, src, "c", "http://x/c.ts")
	// Admin-disable the channel.
	disabled := false
	if err := s.AdminPatchChannel(ctx, chID, store.ChannelPatch{
		EnabledAdmin: store.SetEnabledAdmin(&disabled),
	}); err != nil {
		t.Fatalf("disable: %v", err)
	}

	deps := &streamproxy.Deps{
		Store:    s,
		Settings: streamproxy.StaticSettings{PerUser: 3, PerChannel: 5},
		Logger:   hclog.NewNullLogger(),
	}
	r := newRouter(deps)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/livetv/channels/"+chID+"/stream", nil)
	rr := runRequest(t, r, "u", req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
}

func TestCreateSession_UserCapExceeded(t *testing.T) {
	pool := testutil.StartPG(t)
	s := store.New(pool)
	ctx := context.Background()
	src := seedM3USource(t, ctx, s, "http://x")
	chID := seedChannel(t, ctx, s, src, "c", "http://x/c.ts")

	// Pre-seed two active sessions for the user.
	for i := 0; i < 2; i++ {
		if _, err := s.CreateSession(ctx, store.Session{
			UserID: "user-cap", ChannelID: chID,
			ScopedGrantID: fmt.Sprintf("g%d", i),
			SessionSecret: []byte{0x01},
			UserAgent:     "ua",
		}); err != nil {
			t.Fatalf("seed session: %v", err)
		}
	}

	deps := &streamproxy.Deps{
		Store:    s,
		Settings: streamproxy.StaticSettings{PerUser: 2, PerChannel: 99},
		Logger:   hclog.NewNullLogger(),
	}
	r := newRouter(deps)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/livetv/channels/"+chID+"/stream", nil)
	rr := runRequest(t, r, "user-cap", req)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d", rr.Code)
	}
}

func TestCreateSession_ChannelCapExceeded(t *testing.T) {
	pool := testutil.StartPG(t)
	s := store.New(pool)
	ctx := context.Background()
	src := seedM3USource(t, ctx, s, "http://x")
	chID := seedChannel(t, ctx, s, src, "c", "http://x/c.ts")

	// Two different users each occupying one slot on the channel.
	for i, u := range []string{"u1", "u2"} {
		if _, err := s.CreateSession(ctx, store.Session{
			UserID: u, ChannelID: chID,
			ScopedGrantID: fmt.Sprintf("g%d", i),
			SessionSecret: []byte{0x01},
			UserAgent:     "ua",
		}); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	deps := &streamproxy.Deps{
		Store:    s,
		Settings: streamproxy.StaticSettings{PerUser: 99, PerChannel: 2},
		Logger:   hclog.NewNullLogger(),
	}
	r := newRouter(deps)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/livetv/channels/"+chID+"/stream", nil)
	rr := runRequest(t, r, "u3", req)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
}

func TestCreateSession_ProbeDetectsHLS(t *testing.T) {
	pool := testutil.StartPG(t)
	s := store.New(pool)
	ctx := context.Background()

	// HLS-style upstream (path ends in .m3u8 → fast-path detection).
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte("#EXTM3U\n"))
	}))
	defer upstream.Close()

	src := seedM3USource(t, ctx, s, upstream.URL)
	// Build the channel with kind=unknown and a URL that does NOT end in .m3u8
	// so we exercise the HEAD probe branch.
	id, err := s.UpsertChannelFromM3U(ctx, store.Channel{
		SourceM3UID:     src,
		SourceChannelID: "probe",
		DisplayName:     "probe",
		UpstreamURL:     upstream.URL + "/stream", // no extension → falls to HEAD probe
		UpstreamKind:    "unknown",
	})
	if err != nil {
		t.Fatalf("seed channel: %v", err)
	}

	deps := &streamproxy.Deps{
		Store:    s,
		Settings: streamproxy.StaticSettings{PerUser: 3, PerChannel: 5},
		Logger:   hclog.NewNullLogger(),
	}
	r := newRouter(deps)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/livetv/channels/"+id+"/stream", nil)
	rr := runRequest(t, r, "u", req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}

	// Channel row now records the probed kind.
	got, err := s.GetChannel(ctx, id)
	if err != nil {
		t.Fatalf("get channel: %v", err)
	}
	if got.UpstreamKind != "hls" {
		t.Errorf("kind = %q, want hls", got.UpstreamKind)
	}

	// Playback URL has the .m3u8 suffix.
	var body struct {
		PlaybackURL string `json:"playback_url"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.HasSuffix(body.PlaybackURL, ".m3u8") {
		t.Errorf("playback_url = %q, want suffix .m3u8", body.PlaybackURL)
	}
}
