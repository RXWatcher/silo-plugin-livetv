package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/RXWatcher/continuum-plugin-livetv/internal/store"
	"github.com/RXWatcher/continuum-plugin-livetv/internal/testutil"
)

func TestProgram_GetReturnsDetailsAndCredits(t *testing.T) {
	pool := testutil.StartPG(t)
	srv := newTestServer(pool)
	st := store.New(pool)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)
	if err := st.ReplaceFutureForChannel(ctx, "ch1", []store.Program{
		{ID: "prog-1", Start: now.Add(time.Hour), Stop: now.Add(2 * time.Hour), Title: "Movie", Description: "Big movie"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := st.InsertCredits(ctx, "prog-1", []store.Credit{
		{Kind: "director", Name: "Jane", Position: 0},
		{Kind: "actor", Name: "Ada", Position: 1},
	}); err != nil {
		t.Fatalf("credits: %v", err)
	}

	rr := runRequest(srv, authedReq(http.MethodGet, "/api/v1/livetv/programs/prog-1", "u", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var out struct {
		ID      string `json:"id"`
		Title   string `json:"title"`
		Credits []struct {
			Kind     string `json:"kind"`
			Name     string `json:"name"`
			Position int    `json:"position"`
		} `json:"credits"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Title != "Movie" {
		t.Errorf("title = %q", out.Title)
	}
	if len(out.Credits) != 2 {
		t.Fatalf("credits len = %d, want 2 (%+v)", len(out.Credits), out.Credits)
	}
	if out.Credits[0].Position != 0 || out.Credits[1].Position != 1 {
		t.Errorf("credit ordering wrong: %+v", out.Credits)
	}
}

func TestProgram_GetMissingReturns404(t *testing.T) {
	pool := testutil.StartPG(t)
	srv := newTestServer(pool)
	rr := runRequest(srv, authedReq(http.MethodGet, "/api/v1/livetv/programs/missing", "u", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rr.Code)
	}
}

func TestPrograms_SearchHonoursWindowAndQuery(t *testing.T) {
	pool := testutil.StartPG(t)
	srv := newTestServer(pool)
	st := store.New(pool)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)
	// Seed 3 programmes spanning multiple days. "news" hits the first two
	// titles; the third is far outside the default 48h window.
	if err := st.ReplaceFutureForChannel(ctx, "ch1", []store.Program{
		{Start: now.Add(1 * time.Hour), Stop: now.Add(2 * time.Hour), Title: "Evening News"},
		{Start: now.Add(25 * time.Hour), Stop: now.Add(26 * time.Hour), Title: "Tomorrow News"},
		{Start: now.Add(72 * time.Hour), Stop: now.Add(73 * time.Hour), Title: "Future News"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Default window (48h).
	rr := runRequest(srv, authedReq(http.MethodGet, "/api/v1/livetv/programs/search?q=news", "u", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var env struct {
		Data []map[string]any `json:"data"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &env)
	if len(env.Data) != 2 {
		t.Fatalf("default window hits = %d, want 2", len(env.Data))
	}

	// Tighter window (1h) — only the first program matches.
	to := now.Add(2*time.Hour).Format(time.RFC3339)
	path := "/api/v1/livetv/programs/search?q=news&to=" + url.QueryEscape(to)
	rr = runRequest(srv, authedReq(http.MethodGet, path, "u", nil))
	_ = json.Unmarshal(rr.Body.Bytes(), &env)
	if len(env.Data) != 1 {
		t.Fatalf("tight window hits = %d, want 1", len(env.Data))
	}
	if env.Data[0]["title"] != "Evening News" {
		t.Errorf("first hit = %v, want Evening News", env.Data[0]["title"])
	}
}

func TestPrograms_SearchEmptyQReturnsEmpty(t *testing.T) {
	pool := testutil.StartPG(t)
	srv := newTestServer(pool)
	rr := runRequest(srv, authedReq(http.MethodGet, "/api/v1/livetv/programs/search?q=", "u", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var env struct {
		Data []map[string]any `json:"data"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &env)
	if len(env.Data) != 0 {
		t.Fatalf("data = %d, want 0", len(env.Data))
	}
}
