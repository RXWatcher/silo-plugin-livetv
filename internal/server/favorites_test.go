package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/RXWatcher/continuum-plugin-livetv/internal/store"
	"github.com/RXWatcher/continuum-plugin-livetv/internal/testutil"
)

func TestFavorites_AddListReorderRemoveFlow(t *testing.T) {
	pool := testutil.StartPG(t)
	srv := newTestServer(pool)
	st := store.New(pool)
	ctx := context.Background()

	srcID := seedSource(t, ctx, st)
	a := seedChannel(t, ctx, st, srcID, "a", "G", "")
	b := seedChannel(t, ctx, st, srcID, "b", "G", "")
	c := seedChannel(t, ctx, st, srcID, "c", "G", "")

	// Add 3 favorites.
	for _, id := range []string{a, b, c} {
		rr := runRequest(srv, authedReq(http.MethodPost, "/api/v1/livetv/favorites/"+id, "u1", nil))
		if rr.Code != http.StatusNoContent {
			t.Fatalf("add %s: status = %d body=%s", id, rr.Code, rr.Body.String())
		}
	}

	// List → 3 rows in insertion order with positions 0..2.
	rr := runRequest(srv, authedReq(http.MethodGet, "/api/v1/livetv/favorites", "u1", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("list: status = %d body=%s", rr.Code, rr.Body.String())
	}
	var env struct {
		Data []struct {
			ChannelID string `json:"channel_id"`
			Position  int    `json:"position"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(env.Data) != 3 {
		t.Fatalf("favs = %d, want 3", len(env.Data))
	}
	for i, want := range []string{a, b, c} {
		if env.Data[i].ChannelID != want {
			t.Errorf("favs[%d] = %s, want %s", i, env.Data[i].ChannelID, want)
		}
		if env.Data[i].Position != i {
			t.Errorf("favs[%d] position = %d, want %d", i, env.Data[i].Position, i)
		}
	}

	// Reorder: reverse.
	body := strings.NewReader(`{"channel_ids":["` + c + `","` + b + `","` + a + `"]}`)
	rr = runRequest(srv, authedReq(http.MethodPost, "/api/v1/livetv/favorites/reorder", "u1", body))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("reorder: status = %d body=%s", rr.Code, rr.Body.String())
	}
	rr = runRequest(srv, authedReq(http.MethodGet, "/api/v1/livetv/favorites", "u1", nil))
	_ = json.Unmarshal(rr.Body.Bytes(), &env)
	if env.Data[0].ChannelID != c || env.Data[1].ChannelID != b || env.Data[2].ChannelID != a {
		t.Fatalf("after reorder = %+v", env.Data)
	}

	// Remove b.
	rr = runRequest(srv, authedReq(http.MethodDelete, "/api/v1/livetv/favorites/"+b, "u1", nil))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("remove: status = %d", rr.Code)
	}

	// Final list = [c, a].
	rr = runRequest(srv, authedReq(http.MethodGet, "/api/v1/livetv/favorites", "u1", nil))
	_ = json.Unmarshal(rr.Body.Bytes(), &env)
	if len(env.Data) != 2 {
		t.Fatalf("final favs = %d, want 2", len(env.Data))
	}
	if env.Data[0].ChannelID != c || env.Data[1].ChannelID != a {
		t.Errorf("final order = %+v", env.Data)
	}
}

func TestFavorites_ReorderRejectsUnknownChannels(t *testing.T) {
	pool := testutil.StartPG(t)
	srv := newTestServer(pool)
	st := store.New(pool)
	ctx := context.Background()

	srcID := seedSource(t, ctx, st)
	a := seedChannel(t, ctx, st, srcID, "a", "G", "")
	b := seedChannel(t, ctx, st, srcID, "b", "G", "")
	other := seedChannel(t, ctx, st, srcID, "other", "G", "")

	// Favourite only a + b.
	for _, id := range []string{a, b} {
		rr := runRequest(srv, authedReq(http.MethodPost, "/api/v1/livetv/favorites/"+id, "u1", nil))
		if rr.Code != http.StatusNoContent {
			t.Fatalf("add %s: %d", id, rr.Code)
		}
	}

	// Reorder body includes `other`, which the user has NOT favorited → 400.
	body := strings.NewReader(`{"channel_ids":["` + a + `","` + other + `","` + b + `"]}`)
	rr := runRequest(srv, authedReq(http.MethodPost, "/api/v1/livetv/favorites/reorder", "u1", body))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s, want 400", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), other) {
		t.Errorf("error body should mention offending channel id; got %s", rr.Body.String())
	}

	// State unchanged.
	rr = runRequest(srv, authedReq(http.MethodGet, "/api/v1/livetv/favorites", "u1", nil))
	var env struct {
		Data []struct {
			ChannelID string `json:"channel_id"`
			Position  int    `json:"position"`
		} `json:"data"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &env)
	if len(env.Data) != 2 || env.Data[0].ChannelID != a || env.Data[1].ChannelID != b {
		t.Fatalf("state mutated by rejected reorder: %+v", env.Data)
	}
}

func TestFavorites_AddIdempotent(t *testing.T) {
	pool := testutil.StartPG(t)
	srv := newTestServer(pool)
	st := store.New(pool)
	ctx := context.Background()

	srcID := seedSource(t, ctx, st)
	a := seedChannel(t, ctx, st, srcID, "a", "G", "")

	for i := 0; i < 3; i++ {
		rr := runRequest(srv, authedReq(http.MethodPost, "/api/v1/livetv/favorites/"+a, "u1", nil))
		if rr.Code != http.StatusNoContent {
			t.Fatalf("attempt %d: status = %d", i, rr.Code)
		}
	}
	rr := runRequest(srv, authedReq(http.MethodGet, "/api/v1/livetv/favorites", "u1", nil))
	var env struct {
		Data []map[string]any `json:"data"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &env)
	if len(env.Data) != 1 {
		t.Fatalf("after 3 adds favs = %d, want 1", len(env.Data))
	}
}
