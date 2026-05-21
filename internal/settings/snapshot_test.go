package settings_test

import (
	"context"
	"testing"
	"time"

	"github.com/RXWatcher/continuum-plugin-livetv/internal/settings"
	"github.com/RXWatcher/continuum-plugin-livetv/internal/store"
	"github.com/RXWatcher/continuum-plugin-livetv/internal/testutil"
)

func TestLoad_ReturnsMigrationDefaults(t *testing.T) {
	pool := testutil.StartPG(t)
	st := store.New(pool)
	ctx := context.Background()

	snap, err := settings.Load(ctx, st)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	// Defaults from migration 0001: 6h M3U, 3h XMLTV, 24h guide cap, 3/5
	// per-user/per-channel caps, 60s idle.
	if snap.DefaultM3URefresh() != 6*time.Hour {
		t.Errorf("m3u refresh = %v, want 6h", snap.DefaultM3URefresh())
	}
	if snap.DefaultXMLTVRefresh() != 3*time.Hour {
		t.Errorf("xmltv refresh = %v, want 3h", snap.DefaultXMLTVRefresh())
	}
	if snap.GuideWindowCap() != 24*time.Hour {
		t.Errorf("guide cap = %v, want 24h", snap.GuideWindowCap())
	}
	if snap.PerUserStreamCap() != 3 {
		t.Errorf("per user = %d, want 3", snap.PerUserStreamCap())
	}
	if snap.PerChannelDefaultCap() != 5 {
		t.Errorf("per channel = %d, want 5", snap.PerChannelDefaultCap())
	}
	if snap.SessionIdleTimeout() != 60*time.Second {
		t.Errorf("idle = %v, want 60s", snap.SessionIdleTimeout())
	}
}

func TestApply_OverridesInMemoryValues(t *testing.T) {
	snap := settings.FromValues(settings.SettingsValues{
		DefaultM3URefresh:    1 * time.Hour,
		DefaultXMLTVRefresh:  30 * time.Minute,
		GuideWindowCap:       12 * time.Hour,
		PerUserStreamCap:     2,
		PerChannelDefaultCap: 4,
		SessionIdleTimeout:   5 * time.Minute,
	})
	if snap.PerUserStreamCap() != 2 {
		t.Errorf("per user = %d, want 2", snap.PerUserStreamCap())
	}

	// Apply new values; subsequent reads see the new state.
	snap.Apply(settings.SettingsValues{
		DefaultM3URefresh:    2 * time.Hour,
		DefaultXMLTVRefresh:  45 * time.Minute,
		GuideWindowCap:       18 * time.Hour,
		PerUserStreamCap:     10,
		PerChannelDefaultCap: 20,
		SessionIdleTimeout:   2 * time.Minute,
	})
	if snap.PerUserStreamCap() != 10 {
		t.Errorf("after apply per user = %d, want 10", snap.PerUserStreamCap())
	}
	if snap.SessionIdleTimeout() != 2*time.Minute {
		t.Errorf("after apply idle = %v, want 2m", snap.SessionIdleTimeout())
	}
}

func TestReload_PicksUpStoreEdits(t *testing.T) {
	pool := testutil.StartPG(t)
	st := store.New(pool)
	ctx := context.Background()

	snap, err := settings.Load(ctx, st)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := st.UpdateSettings(ctx, store.SettingsRow{
		DefaultM3URefresh:    4 * time.Hour,
		DefaultXMLTVRefresh:  2 * time.Hour,
		GuideWindowCap:       6 * time.Hour,
		PerUserStreamCap:     7,
		PerChannelDefaultCap: 9,
		SessionIdleTimeout:   90 * time.Second,
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	// Snapshot still has the old values until Reload.
	if snap.PerUserStreamCap() != 3 {
		t.Errorf("pre-reload per user = %d, want stale 3", snap.PerUserStreamCap())
	}
	if err := snap.Reload(ctx); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if snap.PerUserStreamCap() != 7 {
		t.Errorf("post-reload per user = %d, want 7", snap.PerUserStreamCap())
	}
	if snap.SessionIdleTimeout() != 90*time.Second {
		t.Errorf("post-reload idle = %v, want 90s", snap.SessionIdleTimeout())
	}
}

func TestReload_StorelessIsNoOp(t *testing.T) {
	snap := settings.FromValues(settings.SettingsValues{PerUserStreamCap: 42})
	if err := snap.Reload(context.Background()); err != nil {
		t.Fatalf("reload no-op errored: %v", err)
	}
	if snap.PerUserStreamCap() != 42 {
		t.Errorf("per user changed across no-op reload: %d", snap.PerUserStreamCap())
	}
}
