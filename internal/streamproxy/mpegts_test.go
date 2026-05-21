package streamproxy_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/hashicorp/go-hclog"

	"github.com/RXWatcher/continuum-plugin-livetv/internal/store"
	"github.com/RXWatcher/continuum-plugin-livetv/internal/streamproxy"
	"github.com/RXWatcher/continuum-plugin-livetv/internal/testutil"
)

// mintSession inserts a stream_sessions row directly and returns the session
// id plus the cookie value to attach to subsequent requests. Avoids going
// through CreateSession so cap / probe behaviour doesn't leak into byte-proxy
// tests.
func mintSession(t *testing.T, ctx context.Context, s *store.Store, userID, channelID string) (string, string, []byte) {
	t.Helper()
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		t.Fatalf("rand: %v", err)
	}
	created, err := s.CreateSession(ctx, store.Session{
		UserID:        userID,
		ChannelID:     channelID,
		SessionSecret: secret,
		ScopedGrantID: "g",
		UserAgent:     "ua",
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	value := created.ID + "." + hex.EncodeToString(secret)
	return created.ID, value, secret
}

func TestProxyMPEGTS_HappyPath(t *testing.T) {
	pool := testutil.StartPG(t)
	s := store.New(pool)
	ctx := context.Background()

	// Pseudo-random 1 MiB payload.
	payload := make([]byte, 1<<20)
	if _, err := rand.Read(payload); err != nil {
		t.Fatalf("rand: %v", err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "video/mp2t")
		_, _ = w.Write(payload)
	}))
	defer upstream.Close()

	srcID := seedM3USource(t, ctx, s, upstream.URL)
	chID := seedChannel(t, ctx, s, srcID, "ch", upstream.URL)
	sessID, cookieVal, _ := mintSession(t, ctx, s, "u", chID)

	deps := &streamproxy.Deps{
		Store:    s,
		Settings: streamproxy.StaticSettings{PerUser: 3, PerChannel: 5},
		Logger:   hclog.NewNullLogger(),
	}
	r := chi.NewRouter()
	r.Get("/api/v1/livetv/stream/{session_id}.ts", deps.ProxyMPEGTS)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/livetv/stream/"+sessID+".ts", nil)
	req.AddCookie(&http.Cookie{Name: "livetv_stream", Value: cookieVal})
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if got := rr.Header().Get("Content-Type"); got != "video/mp2t" {
		t.Errorf("ct = %q, want video/mp2t", got)
	}
	if !bytes.Equal(rr.Body.Bytes(), payload) {
		t.Errorf("body mismatch: got %d, want %d", rr.Body.Len(), len(payload))
	}

	// Session ended with the expected reason and byte count.
	sess, err := s.GetSession(ctx, sessID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if sess.EndedAt == nil {
		t.Error("session should be ended after stream EOF")
	}
	if sess.EndReason != "client_disconnect" {
		t.Errorf("end_reason = %q, want client_disconnect", sess.EndReason)
	}
	if sess.BytesStreamed < int64(len(payload)) {
		t.Errorf("bytes_streamed = %d, want >= %d", sess.BytesStreamed, len(payload))
	}
}

func TestProxyMPEGTS_NoCookie_Unauthorized(t *testing.T) {
	pool := testutil.StartPG(t)
	s := store.New(pool)
	ctx := context.Background()

	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls++
		_, _ = io.Copy(io.Discard, io.LimitReader(bytes.NewReader([]byte{0x47}), 1))
		_, _ = w.Write([]byte{0x47})
	}))
	defer upstream.Close()
	srcID := seedM3USource(t, ctx, s, upstream.URL)
	chID := seedChannel(t, ctx, s, srcID, "ch", upstream.URL)
	sessID, _, _ := mintSession(t, ctx, s, "u", chID)

	deps := &streamproxy.Deps{
		Store:    s,
		Settings: streamproxy.StaticSettings{PerUser: 3, PerChannel: 5},
		Logger:   hclog.NewNullLogger(),
	}
	r := chi.NewRouter()
	r.Get("/api/v1/livetv/stream/{session_id}.ts", deps.ProxyMPEGTS)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/livetv/stream/"+sessID+".ts", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rr.Code)
	}
	if upstreamCalls != 0 {
		t.Errorf("upstream called %d times; expected 0", upstreamCalls)
	}
}

func TestProxyMPEGTS_UpstreamError(t *testing.T) {
	pool := testutil.StartPG(t)
	s := store.New(pool)
	ctx := context.Background()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer upstream.Close()
	srcID := seedM3USource(t, ctx, s, upstream.URL)
	chID := seedChannel(t, ctx, s, srcID, "ch", upstream.URL)
	sessID, cookieVal, _ := mintSession(t, ctx, s, "u", chID)

	deps := &streamproxy.Deps{
		Store:    s,
		Settings: streamproxy.StaticSettings{PerUser: 3, PerChannel: 5},
		Logger:   hclog.NewNullLogger(),
	}
	r := chi.NewRouter()
	r.Get("/api/v1/livetv/stream/{session_id}.ts", deps.ProxyMPEGTS)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/livetv/stream/"+sessID+".ts", nil)
	req.AddCookie(&http.Cookie{Name: "livetv_stream", Value: cookieVal})
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rr.Code)
	}

	// Give the handler a beat to commit its end-session update under load.
	time.Sleep(50 * time.Millisecond)
	sess, err := s.GetSession(ctx, sessID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if sess.EndedAt == nil || sess.EndReason != "upstream_error" {
		t.Errorf("end_reason = %q (ended_at=%v), want upstream_error", sess.EndReason, sess.EndedAt)
	}
}
