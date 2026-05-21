package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/RXWatcher/continuum-plugin-livetv/internal/store"
	"github.com/RXWatcher/continuum-plugin-livetv/internal/testutil"
)

// seedM3USource creates a parent m3u_source row so channel inserts pass the
// foreign-key check. Returns the source id.
func seedM3USource(t *testing.T, ctx context.Context, s *store.Store) string {
	t.Helper()
	src, err := s.CreateM3USource(ctx, store.M3USource{
		Name: "test-src", URL: "http://x", Enabled: true, RefreshInterval: time.Hour,
	})
	if err != nil {
		t.Fatalf("seed m3u: %v", err)
	}
	return src.ID
}

func TestUpsertChannelFromM3U(t *testing.T) {
	pool := testutil.StartPG(t)
	s := store.New(pool)
	ctx := context.Background()
	srcID := seedM3USource(t, ctx, s)

	id1, err := s.UpsertChannelFromM3U(ctx, store.Channel{
		SourceM3UID: srcID, SourceChannelID: "cnn", DisplayName: "CNN",
		UpstreamURL:      "http://cdn/cnn.ts",
		ChannelNumberSrc: "101", GroupTitleSrc: "News",
		Attrs: map[string]string{"tvg-id": "cnn.us"},
	})
	if err != nil {
		t.Fatalf("upsert insert: %v", err)
	}
	if id1 == "" {
		t.Fatalf("expected non-empty id")
	}

	id2, err := s.UpsertChannelFromM3U(ctx, store.Channel{
		SourceM3UID: srcID, SourceChannelID: "cnn",
		DisplayName:      "CNN International",
		UpstreamURL:      "http://cdn/cnn-intl.ts",
		ChannelNumberSrc: "101", GroupTitleSrc: "News",
		Attrs: map[string]string{"tvg-id": "cnn.us"},
	})
	if err != nil {
		t.Fatalf("upsert update: %v", err)
	}
	if id2 != id1 {
		t.Fatalf("id changed on upsert: %q -> %q", id1, id2)
	}
	got, err := s.GetChannel(ctx, id1)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.DisplayName != "CNN International" {
		t.Fatalf("display_name = %q, want CNN International", got.DisplayName)
	}
}

func TestUpsertChannelPreservesAdminOverrides(t *testing.T) {
	pool := testutil.StartPG(t)
	s := store.New(pool)
	ctx := context.Background()
	srcID := seedM3USource(t, ctx, s)

	id, err := s.UpsertChannelFromM3U(ctx, store.Channel{
		SourceM3UID: srcID, SourceChannelID: "bbc1",
		DisplayName: "BBC One", UpstreamURL: "http://x/bbc1",
		ChannelNumberSrc: "201", GroupTitleSrc: "News",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	adminNum := "1"
	adminGroup := "Favorites"
	disabled := false
	pos := 7
	if err := s.AdminPatchChannel(ctx, id, store.ChannelPatch{
		ChannelNumberAdmin: store.SetChannelNumberAdmin(&adminNum),
		GroupTitleAdmin:    store.SetGroupTitleAdmin(&adminGroup),
		EnabledAdmin:       store.SetEnabledAdmin(&disabled),
		Position:           store.SetPosition(&pos),
	}); err != nil {
		t.Fatalf("admin patch: %v", err)
	}

	// Re-upsert with new src values; overrides must stick.
	if _, err := s.UpsertChannelFromM3U(ctx, store.Channel{
		SourceM3UID: srcID, SourceChannelID: "bbc1",
		DisplayName: "BBC ONE HD", UpstreamURL: "http://x/bbc1-hd",
		ChannelNumberSrc: "202", GroupTitleSrc: "Entertainment",
	}); err != nil {
		t.Fatalf("upsert 2: %v", err)
	}
	got, err := s.GetChannel(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ChannelNumberAdmin == nil || *got.ChannelNumberAdmin != "1" {
		t.Fatalf("admin channel number lost: %+v", got.ChannelNumberAdmin)
	}
	if got.GroupTitleAdmin == nil || *got.GroupTitleAdmin != "Favorites" {
		t.Fatalf("admin group lost: %+v", got.GroupTitleAdmin)
	}
	if got.EnabledAdmin == nil || *got.EnabledAdmin != false {
		t.Fatalf("admin enabled lost: %+v", got.EnabledAdmin)
	}
	if got.Position != 7 {
		t.Fatalf("position lost: %d", got.Position)
	}
	// src columns were updated.
	if got.ChannelNumberSrc != "202" || got.GroupTitleSrc != "Entertainment" || got.DisplayName != "BBC ONE HD" {
		t.Fatalf("src columns not updated: %+v", got)
	}
	// enabled_src is reset to true on every upsert by design.
	if !got.EnabledSrc {
		t.Fatalf("enabled_src should be true after upsert")
	}
}

func TestMarkChannelsMissing(t *testing.T) {
	pool := testutil.StartPG(t)
	s := store.New(pool)
	ctx := context.Background()
	srcID := seedM3USource(t, ctx, s)

	idA, _ := s.UpsertChannelFromM3U(ctx, store.Channel{
		SourceM3UID: srcID, SourceChannelID: "a", DisplayName: "A", UpstreamURL: "u",
	})
	idB, _ := s.UpsertChannelFromM3U(ctx, store.Channel{
		SourceM3UID: srcID, SourceChannelID: "b", DisplayName: "B", UpstreamURL: "u",
	})
	idC, _ := s.UpsertChannelFromM3U(ctx, store.Channel{
		SourceM3UID: srcID, SourceChannelID: "c", DisplayName: "C", UpstreamURL: "u",
	})

	if err := s.MarkChannelsMissing(ctx, srcID, []string{"a", "c"}); err != nil {
		t.Fatalf("mark missing: %v", err)
	}
	checkEnabled := func(id string, want bool) {
		t.Helper()
		ch, err := s.GetChannel(ctx, id)
		if err != nil {
			t.Fatalf("get %s: %v", id, err)
		}
		if ch.EnabledSrc != want {
			t.Fatalf("channel %s enabled_src = %v, want %v", id, ch.EnabledSrc, want)
		}
	}
	checkEnabled(idA, true)
	checkEnabled(idB, false)
	checkEnabled(idC, true)
}

func TestListChannelsForUserAndGroups(t *testing.T) {
	pool := testutil.StartPG(t)
	s := store.New(pool)
	ctx := context.Background()
	srcID := seedM3USource(t, ctx, s)

	mk := func(srcChID, name, group string) string {
		id, err := s.UpsertChannelFromM3U(ctx, store.Channel{
			SourceM3UID: srcID, SourceChannelID: srcChID,
			DisplayName: name, UpstreamURL: "u", GroupTitleSrc: group,
		})
		if err != nil {
			t.Fatalf("upsert %s: %v", srcChID, err)
		}
		return id
	}
	idAlpha := mk("alpha", "Alpha News", "News")
	mk("beta", "Beta Sport", "Sport")
	mk("gamma", "Gamma News", "News")

	// Disable one: gamma -> enabled_admin = false.
	dis := false
	if err := s.AdminPatchChannel(ctx, mk("delta", "Delta Doc", "Docs"), store.ChannelPatch{
		EnabledAdmin: store.SetEnabledAdmin(&dis),
	}); err != nil {
		t.Fatalf("disable delta: %v", err)
	}

	// User-A favorites alpha. Insert directly so this test stays independent
	// of the favorites store (which lands in Task 11).
	if _, err := pool.Exec(ctx, `INSERT INTO user_favorites (user_id, channel_id, position) VALUES ($1, $2, 0)`, "user-a", idAlpha); err != nil {
		t.Fatalf("seed favorite: %v", err)
	}

	all, _, err := s.ListChannelsForUser(ctx, "user-a", "", "", 50, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("got %d channels, want 3 (delta disabled)", len(all))
	}

	news, _, err := s.ListChannelsForUser(ctx, "user-a", "News", "", 50, "")
	if err != nil {
		t.Fatalf("list News: %v", err)
	}
	if len(news) != 2 {
		t.Fatalf("News list = %d, want 2", len(news))
	}

	// search for "Sport"
	sport, _, err := s.ListChannelsForUser(ctx, "user-a", "", "Sport", 50, "")
	if err != nil {
		t.Fatalf("list search: %v", err)
	}
	if len(sport) != 1 || sport[0].DisplayName != "Beta Sport" {
		t.Fatalf("search Sport = %+v", sport)
	}

	// favorite flag
	var alphaSeen bool
	for _, c := range all {
		if c.ID == idAlpha {
			alphaSeen = true
			if !c.HasFavorite {
				t.Fatalf("alpha should be marked favorite")
			}
		} else if c.HasFavorite {
			t.Fatalf("channel %q should not be favorite", c.DisplayName)
		}
	}
	if !alphaSeen {
		t.Fatalf("alpha missing from list")
	}

	groups, err := s.ListGroups(ctx)
	if err != nil {
		t.Fatalf("list groups: %v", err)
	}
	// Expect News + Sport (Docs is gated out by enabled_admin=false).
	if len(groups) != 2 {
		t.Fatalf("groups = %+v, want 2", groups)
	}
}

func TestGetChannelMissing(t *testing.T) {
	pool := testutil.StartPG(t)
	s := store.New(pool)
	ctx := context.Background()
	if _, err := s.GetChannel(ctx, "01HZZNOEXIST"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestEPGKeysCRUD(t *testing.T) {
	pool := testutil.StartPG(t)
	s := store.New(pool)
	ctx := context.Background()
	srcID := seedM3USource(t, ctx, s)
	id, _ := s.UpsertChannelFromM3U(ctx, store.Channel{
		SourceM3UID: srcID, SourceChannelID: "abc", DisplayName: "ABC", UpstreamURL: "u",
	})
	if err := s.AddEPGKey(ctx, id, "abc.us", true); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := s.AddEPGKey(ctx, id, "abc.alt", false); err != nil {
		t.Fatalf("add 2: %v", err)
	}
	keys, err := s.ListEPGKeys(ctx, id)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("keys = %+v", keys)
	}
	if err := s.RemoveEPGKey(ctx, id, "abc.alt"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	keys, _ = s.ListEPGKeys(ctx, id)
	if len(keys) != 1 || keys[0] != "abc.us" {
		t.Fatalf("post-remove keys = %+v", keys)
	}
}
