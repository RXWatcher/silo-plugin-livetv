package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/ContinuumApp/continuum-plugin-livetv/internal/store"
	"github.com/ContinuumApp/continuum-plugin-livetv/internal/testutil"
)

func TestGetSettings_ReturnsMigrationDefaults(t *testing.T) {
	pool := testutil.StartPG(t)
	s := store.New(pool)
	ctx := context.Background()

	got, err := s.GetSettings(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	// Migration 0001 seeds (6h, 3h, 24h, 3, 5, 60s); see migrate/files.
	if got.DefaultM3URefresh != 6*time.Hour {
		t.Errorf("m3u = %v, want 6h", got.DefaultM3URefresh)
	}
	if got.DefaultXMLTVRefresh != 3*time.Hour {
		t.Errorf("xmltv = %v, want 3h", got.DefaultXMLTVRefresh)
	}
	if got.GuideWindowCap != 24*time.Hour {
		t.Errorf("guide = %v, want 24h", got.GuideWindowCap)
	}
	if got.PerUserStreamCap != 3 || got.PerChannelDefaultCap != 5 {
		t.Errorf("caps = %d/%d, want 3/5", got.PerUserStreamCap, got.PerChannelDefaultCap)
	}
	if got.SessionIdleTimeout != 60*time.Second {
		t.Errorf("idle = %v, want 60s", got.SessionIdleTimeout)
	}
}

func TestUpdateSettings_RoundTrip(t *testing.T) {
	pool := testutil.StartPG(t)
	s := store.New(pool)
	ctx := context.Background()

	row := store.SettingsRow{
		DefaultM3URefresh:    8 * time.Hour,
		DefaultXMLTVRefresh:  4 * time.Hour,
		GuideWindowCap:       18 * time.Hour,
		PerUserStreamCap:     6,
		PerChannelDefaultCap: 10,
		SessionIdleTimeout:   2 * time.Minute,
	}
	if err := s.UpdateSettings(ctx, row); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, err := s.GetSettings(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != row {
		t.Fatalf("round-trip mismatch: %+v vs %+v", got, row)
	}
}
