package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/ContinuumApp/continuum-plugin-livetv/internal/store"
	"github.com/ContinuumApp/continuum-plugin-livetv/internal/testutil"
)

// seedChannels creates n channels under a fresh m3u source and returns the
// channel ids in insertion order. Used by the favorites + recent tests so
// the user_favorites / user_recent FKs are satisfied.
func seedChannels(t *testing.T, ctx context.Context, s *store.Store, names ...string) []string {
	t.Helper()
	srcID := seedM3USource(t, ctx, s)
	out := make([]string, len(names))
	for i, name := range names {
		id, err := s.UpsertChannelFromM3U(ctx, store.Channel{
			SourceM3UID:     srcID,
			SourceChannelID: name,
			DisplayName:     name,
			UpstreamURL:     "http://x/" + name,
		})
		if err != nil {
			t.Fatalf("upsert %s: %v", name, err)
		}
		out[i] = id
	}
	return out
}

func TestFavoritesAddListRemove(t *testing.T) {
	pool := testutil.StartPG(t)
	s := store.New(pool)
	ctx := context.Background()
	ids := seedChannels(t, ctx, s, "a", "b", "c")

	for _, id := range ids {
		if err := s.AddFavorite(ctx, "u1", id); err != nil {
			t.Fatalf("add %s: %v", id, err)
		}
	}
	// Idempotency: re-adding should not change the row count.
	if err := s.AddFavorite(ctx, "u1", ids[1]); err != nil {
		t.Fatalf("re-add: %v", err)
	}

	favs, err := s.ListFavorites(ctx, "u1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(favs) != 3 {
		t.Fatalf("favs = %d, want 3", len(favs))
	}
	// Insertion order: positions assigned 0, 1, 2.
	for i, f := range favs {
		if f.Position != i {
			t.Fatalf("fav[%d] position = %d, want %d", i, f.Position, i)
		}
		if f.ChannelID != ids[i] {
			t.Fatalf("fav[%d] channel = %s, want %s", i, f.ChannelID, ids[i])
		}
	}

	// Remove the middle one.
	if err := s.RemoveFavorite(ctx, "u1", ids[1]); err != nil {
		t.Fatalf("remove: %v", err)
	}
	// Idempotent: removing again is a no-op.
	if err := s.RemoveFavorite(ctx, "u1", ids[1]); err != nil {
		t.Fatalf("re-remove: %v", err)
	}
	favs, _ = s.ListFavorites(ctx, "u1")
	if len(favs) != 2 {
		t.Fatalf("after remove favs = %d, want 2", len(favs))
	}
	if favs[0].ChannelID != ids[0] || favs[1].ChannelID != ids[2] {
		t.Fatalf("after-remove order: %+v", favs)
	}
}

func TestReorderFavorites(t *testing.T) {
	pool := testutil.StartPG(t)
	s := store.New(pool)
	ctx := context.Background()
	ids := seedChannels(t, ctx, s, "a", "b", "c")

	for _, id := range ids {
		if err := s.AddFavorite(ctx, "u1", id); err != nil {
			t.Fatalf("add %s: %v", id, err)
		}
	}

	// Reverse order.
	reordered := []string{ids[2], ids[1], ids[0]}
	if err := s.ReorderFavorites(ctx, "u1", reordered); err != nil {
		t.Fatalf("reorder: %v", err)
	}
	favs, _ := s.ListFavorites(ctx, "u1")
	if len(favs) != 3 {
		t.Fatalf("favs = %d", len(favs))
	}
	for i, f := range favs {
		if f.ChannelID != reordered[i] {
			t.Fatalf("favs[%d] = %s, want %s (positions: %+v)", i, f.ChannelID, reordered[i], favs)
		}
		if f.Position != i {
			t.Fatalf("favs[%d] position = %d, want %d", i, f.Position, i)
		}
	}

	// Empty input is a no-op (order preserved).
	if err := s.ReorderFavorites(ctx, "u1", nil); err != nil {
		t.Fatalf("reorder nil: %v", err)
	}
	favs2, _ := s.ListFavorites(ctx, "u1")
	if len(favs2) != 3 || favs2[0].ChannelID != reordered[0] {
		t.Fatalf("reorder nil mutated state: %+v", favs2)
	}
}

func TestMarkTunedAndListRecent(t *testing.T) {
	pool := testutil.StartPG(t)
	s := store.New(pool)
	ctx := context.Background()
	ids := seedChannels(t, ctx, s, "a", "b", "c")

	// Tune a, then b, then a again. ListRecent should be: a, b (a most recent).
	if err := s.MarkTuned(ctx, "u1", ids[0]); err != nil {
		t.Fatalf("tune a: %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	if err := s.MarkTuned(ctx, "u1", ids[1]); err != nil {
		t.Fatalf("tune b: %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	if err := s.MarkTuned(ctx, "u1", ids[0]); err != nil {
		t.Fatalf("re-tune a: %v", err)
	}

	// Confirm no duplicate row was created for channel a.
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM user_recent WHERE user_id='u1' AND channel_id=$1`, ids[0]).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("user_recent has %d rows for channel a, want 1 (re-tune must upsert)", count)
	}

	recent, err := s.ListRecent(ctx, "u1", 10)
	if err != nil {
		t.Fatalf("list recent: %v", err)
	}
	if len(recent) != 2 {
		t.Fatalf("recent = %d, want 2", len(recent))
	}
	if recent[0].ChannelID != ids[0] {
		t.Fatalf("recent[0] = %s, want %s (a, most recently re-tuned)", recent[0].ChannelID, ids[0])
	}
	if recent[1].ChannelID != ids[1] {
		t.Fatalf("recent[1] = %s, want %s", recent[1].ChannelID, ids[1])
	}
	if !recent[0].LastTunedAt.After(recent[1].LastTunedAt) {
		t.Fatalf("LastTunedAt ordering wrong: %v vs %v", recent[0].LastTunedAt, recent[1].LastTunedAt)
	}
}
