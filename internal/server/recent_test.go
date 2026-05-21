package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/RXWatcher/continuum-plugin-livetv/internal/store"
	"github.com/RXWatcher/continuum-plugin-livetv/internal/testutil"
)

func TestRecent_ListReturnsTunedChannelsNewestFirst(t *testing.T) {
	pool := testutil.StartPG(t)
	srv := newTestServer(pool)
	st := store.New(pool)
	ctx := context.Background()

	srcID := seedSource(t, ctx, st)
	a := seedChannel(t, ctx, st, srcID, "a", "G", "")
	b := seedChannel(t, ctx, st, srcID, "b", "G", "")
	c := seedChannel(t, ctx, st, srcID, "c", "G", "")

	// Tune a, b, c with spacing so the ordering is deterministic.
	for _, id := range []string{a, b, c} {
		if err := st.MarkTuned(ctx, "u1", id); err != nil {
			t.Fatalf("tune %s: %v", id, err)
		}
		time.Sleep(5 * time.Millisecond)
	}

	rr := runRequest(srv, authedReq(http.MethodGet, "/api/v1/livetv/recent", "u1", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var env struct {
		Data []struct {
			ChannelID   string    `json:"channel_id"`
			LastTunedAt time.Time `json:"last_tuned_at"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(env.Data) != 3 {
		t.Fatalf("data = %d, want 3", len(env.Data))
	}
	// Newest first: c, b, a.
	wantOrder := []string{c, b, a}
	for i, want := range wantOrder {
		if env.Data[i].ChannelID != want {
			t.Errorf("data[%d] = %s, want %s", i, env.Data[i].ChannelID, want)
		}
	}
}

func TestRecent_LimitClamping(t *testing.T) {
	pool := testutil.StartPG(t)
	srv := newTestServer(pool)
	st := store.New(pool)
	ctx := context.Background()

	srcID := seedSource(t, ctx, st)
	ids := make([]string, 0, 3)
	for _, name := range []string{"a", "b", "c"} {
		ids = append(ids, seedChannel(t, ctx, st, srcID, name, "G", ""))
	}
	for _, id := range ids {
		if err := st.MarkTuned(ctx, "u1", id); err != nil {
			t.Fatalf("tune: %v", err)
		}
		time.Sleep(2 * time.Millisecond)
	}

	rr := runRequest(srv, authedReq(http.MethodGet, "/api/v1/livetv/recent?limit=2", "u1", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var env struct {
		Data []map[string]any `json:"data"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &env)
	if len(env.Data) != 2 {
		t.Fatalf("data = %d, want 2 (limit=2)", len(env.Data))
	}
}
