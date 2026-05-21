package streamproxy_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/hashicorp/go-hclog"

	"github.com/RXWatcher/continuum-plugin-livetv/internal/refresh"
	"github.com/RXWatcher/continuum-plugin-livetv/internal/store"
	"github.com/RXWatcher/continuum-plugin-livetv/internal/streamproxy"
	"github.com/RXWatcher/continuum-plugin-livetv/internal/testutil"
)

// readFixture loads a file from testdata/. Fails the test on read error so
// individual cases don't have to plumb errors.
func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return b
}

func TestSignVerifySegment_RoundTrip(t *testing.T) {
	secret := []byte("00112233445566778899aabbccddeeff")
	token := streamproxy.SignSegment(secret, "http://up/seg1.ts", time.Now().Add(time.Minute))
	got, err := streamproxy.VerifySegment(secret, token)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got != "http://up/seg1.ts" {
		t.Errorf("got %q", got)
	}
}

func TestVerifySegment_Tampered(t *testing.T) {
	secret := []byte("00112233445566778899aabbccddeeff")
	token := streamproxy.SignSegment(secret, "http://up/seg1.ts", time.Now().Add(time.Minute))
	// Flip a byte in the signature (rightmost of the dot-split). Use a stable
	// mutation that survives base64 round-trip: change the last visible char.
	idx := strings.LastIndex(token, ".")
	if idx < 0 {
		t.Fatal("token missing '.'")
	}
	mutated := token[:idx+1] + flipChar(token[idx+1:])
	if _, err := streamproxy.VerifySegment(secret, mutated); err == nil {
		t.Fatal("expected verify failure for tampered signature")
	}
}

// flipChar returns s with its last char swapped to a different one in the
// base64-url alphabet so the resulting string still parses but the HMAC
// mismatch is unmistakable.
func flipChar(s string) string {
	if s == "" {
		return s
	}
	last := s[len(s)-1]
	var swap byte = 'A'
	if last == 'A' {
		swap = 'B'
	}
	return s[:len(s)-1] + string(swap)
}

func TestVerifySegment_Expired(t *testing.T) {
	secret := []byte("00112233445566778899aabbccddeeff")
	token := streamproxy.SignSegment(secret, "http://up/seg1.ts", time.Now().Add(-time.Second))
	if _, err := streamproxy.VerifySegment(secret, token); err == nil {
		t.Fatal("expected verify failure for expired token")
	}
}

func TestRewritePlaylist_Media(t *testing.T) {
	body := readFixture(t, "media.m3u8")
	secret := []byte("00112233445566778899aabbccddeeff")
	base, _ := url.Parse("http://upstream-base/")
	out, err := streamproxy.RewritePlaylist(bytes.NewReader(body), base, "SESS123", secret, "/api/v1/livetv", time.Minute)
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")

	wantPreservedPrefix := []string{
		"#EXTM3U",
		"#EXT-X-VERSION:3",
		"#EXT-X-TARGETDURATION:6",
		"#EXT-X-MEDIA-SEQUENCE:1",
		"#EXTINF:6.0,",
	}
	for i, want := range wantPreservedPrefix {
		if lines[i] != want {
			t.Errorf("line %d = %q, want %q", i, lines[i], want)
		}
	}

	// We expect exactly two rewritten URIs and they decode back to the
	// absolute upstream URLs.
	var rewritten []string
	for _, line := range lines {
		if strings.HasPrefix(line, "/api/v1/livetv/stream/SESS123/segment?u=") {
			rewritten = append(rewritten, line)
		}
	}
	if len(rewritten) != 2 {
		t.Fatalf("rewritten segments = %d, want 2 (lines=%v)", len(rewritten), lines)
	}
	for i, line := range rewritten {
		q := line[strings.Index(line, "?u=")+3:]
		tok, err := url.QueryUnescape(q)
		if err != nil {
			t.Fatalf("unescape: %v", err)
		}
		got, err := streamproxy.VerifySegment(secret, tok)
		if err != nil {
			t.Fatalf("verify %d: %v", i, err)
		}
		want := "http://upstream-base/seg" + map[int]string{0: "1", 1: "2"}[i] + ".ts"
		if got != want {
			t.Errorf("segment %d uri = %q, want %q", i, got, want)
		}
	}
}

func TestRewritePlaylist_Master(t *testing.T) {
	body := readFixture(t, "master.m3u8")
	secret := []byte("00112233445566778899aabbccddeeff")
	base, _ := url.Parse("http://upstream-base/master.m3u8")
	out, err := streamproxy.RewritePlaylist(bytes.NewReader(body), base, "S", secret, "/api/v1/livetv", time.Minute)
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	var rewritten []string
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if strings.HasPrefix(line, "/api/v1/livetv/stream/S/segment?u=") {
			rewritten = append(rewritten, line)
		}
	}
	if len(rewritten) != 2 {
		t.Fatalf("rewritten variants = %d, want 2", len(rewritten))
	}
	for i, line := range rewritten {
		q := line[strings.Index(line, "?u=")+3:]
		tok, _ := url.QueryUnescape(q)
		got, err := streamproxy.VerifySegment(secret, tok)
		if err != nil {
			t.Fatalf("verify: %v", err)
		}
		want := "http://upstream-base/" + map[int]string{0: "high", 1: "low"}[i] + ".m3u8"
		if got != want {
			t.Errorf("variant %d = %q, want %q", i, got, want)
		}
	}
}

func TestProxyHLSSegment_HappyPath(t *testing.T) {
	pool := testutil.StartPG(t)
	s := store.New(pool)
	ctx := context.Background()

	payload := bytes.Repeat([]byte{0x47}, 1024)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "video/mp2t")
		_, _ = w.Write(payload)
	}))
	defer upstream.Close()

	srcID := seedM3USource(t, ctx, s, upstream.URL)
	chID := seedChannel(t, ctx, s, srcID, "ch", upstream.URL+"/playlist.m3u8")
	sessID, cookieVal, secret := mintSession(t, ctx, s, "u", chID)

	deps := &streamproxy.Deps{
		Store:    s,
		Settings: streamproxy.StaticSettings{PerUser: 3, PerChannel: 5},
		Logger:   hclog.NewNullLogger(),
	}
	r := chi.NewRouter()
	r.Get("/api/v1/livetv/stream/{session_id}/segment", deps.ProxyHLSSegment)

	token := streamproxy.SignSegment(secret, upstream.URL+"/segment.ts", time.Now().Add(time.Minute))
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/livetv/stream/"+sessID+"/segment?u="+url.QueryEscape(token), nil)
	req.AddCookie(&http.Cookie{Name: "livetv_stream", Value: cookieVal})
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if !bytes.Equal(rr.Body.Bytes(), payload) {
		t.Errorf("body mismatch: got %d want %d", rr.Body.Len(), len(payload))
	}
}

func TestProxyHLSSegment_TamperedToken(t *testing.T) {
	pool := testutil.StartPG(t)
	s := store.New(pool)
	ctx := context.Background()

	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls++
		_, _ = w.Write([]byte("nope"))
	}))
	defer upstream.Close()
	srcID := seedM3USource(t, ctx, s, upstream.URL)
	chID := seedChannel(t, ctx, s, srcID, "ch", upstream.URL)
	sessID, cookieVal, secret := mintSession(t, ctx, s, "u", chID)

	deps := &streamproxy.Deps{
		Store:    s,
		Settings: streamproxy.StaticSettings{PerUser: 3, PerChannel: 5},
		Logger:   hclog.NewNullLogger(),
	}
	r := chi.NewRouter()
	r.Get("/api/v1/livetv/stream/{session_id}/segment", deps.ProxyHLSSegment)

	good := streamproxy.SignSegment(secret, upstream.URL+"/x.ts", time.Now().Add(time.Minute))
	bad := good[:len(good)-1] + "A"
	if good == bad {
		bad = good[:len(good)-1] + "B"
	}
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/livetv/stream/"+sessID+"/segment?u="+url.QueryEscape(bad), nil)
	req.AddCookie(&http.Cookie{Name: "livetv_stream", Value: cookieVal})
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rr.Code)
	}
	if upstreamCalls != 0 {
		t.Errorf("upstream called for tampered token")
	}
}

func TestProxyHLSSegment_Expired(t *testing.T) {
	pool := testutil.StartPG(t)
	s := store.New(pool)
	ctx := context.Background()
	srcID := seedM3USource(t, ctx, s, "http://x")
	chID := seedChannel(t, ctx, s, srcID, "ch", "http://x/c.ts")
	sessID, cookieVal, secret := mintSession(t, ctx, s, "u", chID)

	deps := &streamproxy.Deps{
		Store:    s,
		Settings: streamproxy.StaticSettings{PerUser: 3, PerChannel: 5},
		Logger:   hclog.NewNullLogger(),
	}
	r := chi.NewRouter()
	r.Get("/api/v1/livetv/stream/{session_id}/segment", deps.ProxyHLSSegment)

	token := streamproxy.SignSegment(secret, "http://x/expired.ts", time.Now().Add(-time.Second))
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/livetv/stream/"+sessID+"/segment?u="+url.QueryEscape(token), nil)
	req.AddCookie(&http.Cookie{Name: "livetv_stream", Value: cookieVal})
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rr.Code)
	}
}

func TestProxyHLSSegment_IdleReaperEndsSession(t *testing.T) {
	pool := testutil.StartPG(t)
	s := store.New(pool)
	ctx := context.Background()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte{0x47})
	}))
	defer upstream.Close()
	srcID := seedM3USource(t, ctx, s, upstream.URL)
	chID := seedChannel(t, ctx, s, srcID, "ch", upstream.URL+"/seg.ts")
	sessID, cookieVal, secret := mintSession(t, ctx, s, "u", chID)

	settings := streamproxy.StaticSettings{
		PerUser: 3, PerChannel: 5, IdleTimeout: time.Minute,
	}

	// Simulate idleness by backdating last_byte_at by 2*timeout.
	if _, err := pool.Exec(ctx,
		`UPDATE stream_sessions SET last_byte_at = $1 WHERE id = $2`,
		time.Now().UTC().Add(-2*settings.IdleTimeout), sessID); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	if err := refresh.ReapIdle(ctx, s, settings.SessionIdleTimeout(), hclog.NewNullLogger()); err != nil {
		t.Fatalf("reap: %v", err)
	}

	deps := &streamproxy.Deps{
		Store:    s,
		Settings: settings,
		Logger:   hclog.NewNullLogger(),
	}
	r := chi.NewRouter()
	r.Get("/api/v1/livetv/stream/{session_id}/segment", deps.ProxyHLSSegment)

	token := streamproxy.SignSegment(secret, upstream.URL+"/seg.ts", time.Now().Add(time.Minute))
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/livetv/stream/"+sessID+"/segment?u="+url.QueryEscape(token), nil)
	req.AddCookie(&http.Cookie{Name: "livetv_stream", Value: cookieVal})
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (session reaped)", rr.Code)
	}
}

func TestProxyHLSPlaylist_HappyPath(t *testing.T) {
	pool := testutil.StartPG(t)
	s := store.New(pool)
	ctx := context.Background()

	upstreamBody := readFixture(t, "media.m3u8")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write(upstreamBody)
	}))
	defer upstream.Close()

	srcID := seedM3USource(t, ctx, s, upstream.URL)
	chID := seedChannel(t, ctx, s, srcID, "ch", upstream.URL+"/playlist.m3u8")
	sessID, cookieVal, _ := mintSession(t, ctx, s, "u", chID)

	deps := &streamproxy.Deps{
		Store:    s,
		Settings: streamproxy.StaticSettings{PerUser: 3, PerChannel: 5},
		Logger:   hclog.NewNullLogger(),
	}
	r := chi.NewRouter()
	r.Get("/api/v1/livetv/stream/{session_id}.m3u8", deps.ProxyHLSPlaylist)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/livetv/stream/"+sessID+".m3u8", nil)
	req.AddCookie(&http.Cookie{Name: "livetv_stream", Value: cookieVal})
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); got != "application/vnd.apple.mpegurl" {
		t.Errorf("ct = %q", got)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "/api/v1/livetv/stream/"+sessID+"/segment?u=") {
		t.Errorf("rewritten body missing segment URLs:\n%s", body)
	}
}
