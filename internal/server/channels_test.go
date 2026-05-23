package server_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/RXWatcher/silo-plugin-livetv/internal/server"
	"github.com/RXWatcher/silo-plugin-livetv/internal/store"
	"github.com/RXWatcher/silo-plugin-livetv/internal/streamproxy"
	"github.com/RXWatcher/silo-plugin-livetv/internal/testutil"
)

// seedSource creates a parent m3u_sources row so channel inserts pass the FK.
func seedSource(t *testing.T, ctx context.Context, s *store.Store) string {
	t.Helper()
	src, err := s.CreateM3USource(ctx, store.M3USource{
		Name: "test-src", URL: "http://x", Enabled: true, RefreshInterval: time.Hour,
	})
	if err != nil {
		t.Fatalf("seed source: %v", err)
	}
	return src.ID
}

// seedChannel inserts a single channel and returns its id. Optional group/num
// arguments customise the row.
func seedChannel(t *testing.T, ctx context.Context, s *store.Store, srcID, name, group, num string) string {
	t.Helper()
	id, err := s.UpsertChannelFromM3U(ctx, store.Channel{
		SourceM3UID:      srcID,
		SourceChannelID:  name,
		DisplayName:      name,
		UpstreamURL:      "http://x/" + name,
		ChannelNumberSrc: num,
		GroupTitleSrc:    group,
	})
	if err != nil {
		t.Fatalf("seed channel %s: %v", name, err)
	}
	return id
}

// newTestServer builds a *server.Server backed by the supplied pool. Stream
// is left nil — the user API endpoints don't need the stream-proxy plumbing.
func newTestServer(pool *pgxpool.Pool) *server.Server {
	st := store.New(pool)
	return &server.Server{
		Store:    st,
		Settings: streamproxy.StaticSettings{PerUser: 3, PerChannel: 5, GuideWindow: 24 * time.Hour},
		Logger:   hclog.NewNullLogger(),
	}
}

// authedReq builds a request with the X-Silo-User-Id header set so the
// RequireSession middleware lets it through. body may be nil.
func authedReq(method, path, userID string, body io.Reader) *http.Request {
	r := httptest.NewRequest(method, path, body)
	r.Header.Set("X-Silo-User-Id", userID)
	return r
}

// runRequest issues req against srv and returns the recorder.
func runRequest(srv *server.Server, req *http.Request) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rr, req)
	return rr
}

func TestChannels_ListWithFavoritesAndFilters(t *testing.T) {
	pool := testutil.StartPG(t)
	srv := newTestServer(pool)
	st := store.New(pool)
	ctx := context.Background()

	srcID := seedSource(t, ctx, st)
	alpha := seedChannel(t, ctx, st, srcID, "Alpha News", "News", "101")
	_ = seedChannel(t, ctx, st, srcID, "Beta Sport", "Sport", "201")
	_ = seedChannel(t, ctx, st, srcID, "Gamma News", "News", "102")

	// Favourite alpha for user-a so we can assert the flag.
	if err := st.AddFavorite(ctx, "user-a", alpha); err != nil {
		t.Fatalf("add favorite: %v", err)
	}

	// Bare list.
	rr := runRequest(srv, authedReq(http.MethodGet, "/api/v1/livetv/channels", "user-a", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var env struct {
		Data       []map[string]any `json:"data"`
		NextCursor string           `json:"next_cursor"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v body=%s", err, rr.Body.String())
	}
	if len(env.Data) != 3 {
		t.Fatalf("data = %d rows, want 3", len(env.Data))
	}
	// alpha row should carry is_favorite=true; the others false.
	for _, row := range env.Data {
		if row["id"].(string) == alpha {
			if row["is_favorite"] != true {
				t.Errorf("alpha is_favorite = %v, want true", row["is_favorite"])
			}
		} else if row["is_favorite"] == true {
			t.Errorf("non-alpha row %q marked favorite", row["display_name"])
		}
	}

	// Group filter.
	rr = runRequest(srv, authedReq(http.MethodGet, "/api/v1/livetv/channels?group=News", "user-a", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &env)
	if len(env.Data) != 2 {
		t.Fatalf("group=News rows = %d, want 2", len(env.Data))
	}

	// q search.
	rr = runRequest(srv, authedReq(http.MethodGet, "/api/v1/livetv/channels?q=Sport", "user-a", nil))
	_ = json.Unmarshal(rr.Body.Bytes(), &env)
	if len(env.Data) != 1 || env.Data[0]["display_name"].(string) != "Beta Sport" {
		t.Fatalf("q=Sport rows = %+v", env.Data)
	}
}

func TestChannels_PaginationCursor(t *testing.T) {
	pool := testutil.StartPG(t)
	srv := newTestServer(pool)
	st := store.New(pool)
	ctx := context.Background()

	srcID := seedSource(t, ctx, st)
	// Seed 5 channels.
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		_ = seedChannel(t, ctx, st, srcID, n, "G", "100")
	}

	// Limit 2 → expect a next_cursor.
	rr := runRequest(srv, authedReq(http.MethodGet, "/api/v1/livetv/channels?limit=2", "u", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var env struct {
		Data       []map[string]any `json:"data"`
		NextCursor string           `json:"next_cursor"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(env.Data) != 2 {
		t.Fatalf("data = %d, want 2", len(env.Data))
	}
	if env.NextCursor == "" {
		t.Fatalf("next_cursor empty — pagination not triggered")
	}

	// Final page request should drain the rest and stop producing a cursor.
	rr = runRequest(srv, authedReq(http.MethodGet, "/api/v1/livetv/channels?limit=100&cursor="+env.NextCursor, "u", nil))
	var env2 struct {
		Data       []map[string]any `json:"data"`
		NextCursor string           `json:"next_cursor"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &env2); err != nil {
		t.Fatalf("decode 2: %v", err)
	}
	if len(env2.Data) != 3 {
		t.Fatalf("page 2 rows = %d, want 3 (remaining channels)", len(env2.Data))
	}
	if env2.NextCursor != "" {
		t.Fatalf("page 2 next_cursor = %q, want empty", env2.NextCursor)
	}
}

func TestChannels_GetMissingReturns404(t *testing.T) {
	pool := testutil.StartPG(t)
	srv := newTestServer(pool)

	rr := runRequest(srv, authedReq(http.MethodGet, "/api/v1/livetv/channels/nope", "u", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestChannels_GetReturnsChannel(t *testing.T) {
	pool := testutil.StartPG(t)
	srv := newTestServer(pool)
	st := store.New(pool)
	ctx := context.Background()

	srcID := seedSource(t, ctx, st)
	id := seedChannel(t, ctx, st, srcID, "Alpha", "News", "101")

	rr := runRequest(srv, authedReq(http.MethodGet, "/api/v1/livetv/channels/"+id, "u", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out["id"] != id {
		t.Errorf("id = %v, want %s", out["id"], id)
	}
	if out["display_name"] != "Alpha" {
		t.Errorf("display_name = %v", out["display_name"])
	}
	if out["channel_number"] != "101" {
		t.Errorf("channel_number = %v, want 101", out["channel_number"])
	}
}

func TestChannels_UnauthorizedWithoutHeader(t *testing.T) {
	pool := testutil.StartPG(t)
	srv := newTestServer(pool)

	// Build a bare request with no X-Silo-User-Id header.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/livetv/channels", nil)
	rr := runRequest(srv, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}
