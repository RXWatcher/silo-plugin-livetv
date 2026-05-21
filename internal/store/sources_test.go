package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ContinuumApp/continuum-plugin-livetv/internal/store"
	"github.com/ContinuumApp/continuum-plugin-livetv/internal/testutil"
)

func TestM3USourceCRUD(t *testing.T) {
	pool := testutil.StartPG(t)
	s := store.New(pool)
	ctx := context.Background()

	src := store.M3USource{
		Name:            "primary",
		URL:             "http://example.com/playlist.m3u",
		HTTPHeaders:     map[string]string{"User-Agent": "livetv/1.0", "X-Tok": "abc"},
		Enabled:         true,
		RefreshInterval: 6 * time.Hour,
	}
	created, err := s.CreateM3USource(ctx, src)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ID == "" {
		t.Fatalf("expected ULID id assigned")
	}

	got, err := s.GetM3USource(ctx, created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != src.Name || got.URL != src.URL || !got.Enabled {
		t.Fatalf("get mismatch: %+v", got)
	}
	if got.RefreshInterval != 6*time.Hour {
		t.Fatalf("refresh_interval = %s, want 6h", got.RefreshInterval)
	}
	if len(got.HTTPHeaders) != 2 || got.HTTPHeaders["User-Agent"] != "livetv/1.0" {
		t.Fatalf("headers mismatch: %+v", got.HTTPHeaders)
	}

	got.Name = "primary-renamed"
	got.RefreshInterval = 12 * time.Hour
	if err := s.UpdateM3USource(ctx, got); err != nil {
		t.Fatalf("update: %v", err)
	}
	again, err := s.GetM3USource(ctx, got.ID)
	if err != nil {
		t.Fatalf("get post-update: %v", err)
	}
	if again.Name != "primary-renamed" || again.RefreshInterval != 12*time.Hour {
		t.Fatalf("update did not stick: %+v", again)
	}

	when := time.Now().UTC().Truncate(time.Second)
	if err := s.MarkM3UStatus(ctx, got.ID, "200 OK", "etag-abc", "Mon, 01 Jan 2024 00:00:00 GMT", when); err != nil {
		t.Fatalf("mark status: %v", err)
	}
	statusGot, err := s.GetM3USource(ctx, got.ID)
	if err != nil {
		t.Fatalf("get post-mark: %v", err)
	}
	if statusGot.LastStatus != "200 OK" || statusGot.ETag != "etag-abc" ||
		statusGot.LastModified != "Mon, 01 Jan 2024 00:00:00 GMT" {
		t.Fatalf("mark fields missing: %+v", statusGot)
	}
	if statusGot.LastRefreshedAt == nil || !statusGot.LastRefreshedAt.Equal(when) {
		t.Fatalf("last_refreshed_at = %v, want %v", statusGot.LastRefreshedAt, when)
	}
	// Unrelated columns untouched.
	if statusGot.Name != "primary-renamed" || statusGot.RefreshInterval != 12*time.Hour {
		t.Fatalf("mark clobbered unrelated columns: %+v", statusGot)
	}

	// List finds the row.
	list, err := s.ListM3USources(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("list len = %d, want 1", len(list))
	}

	if err := s.DeleteM3USource(ctx, got.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.GetM3USource(ctx, got.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("post-delete get = %v, want ErrNotFound", err)
	}
}

func TestXMLTVSourceCRUD(t *testing.T) {
	pool := testutil.StartPG(t)
	s := store.New(pool)
	ctx := context.Background()

	src := store.XMLTVSource{
		Name:            "guide",
		URL:             "http://example.com/epg.xml.gz",
		HTTPHeaders:     map[string]string{"Accept": "application/xml"},
		Enabled:         true,
		RefreshInterval: 3 * time.Hour,
		Gzip:            true,
	}
	created, err := s.CreateXMLTVSource(ctx, src)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ID == "" {
		t.Fatalf("expected ULID id assigned")
	}

	got, err := s.GetXMLTVSource(ctx, created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != src.Name || got.URL != src.URL || !got.Gzip {
		t.Fatalf("get mismatch: %+v", got)
	}
	if got.RefreshInterval != 3*time.Hour {
		t.Fatalf("refresh_interval = %s, want 3h", got.RefreshInterval)
	}
	if got.HTTPHeaders["Accept"] != "application/xml" {
		t.Fatalf("headers mismatch: %+v", got.HTTPHeaders)
	}

	got.Gzip = false
	got.RefreshInterval = 90 * time.Minute
	if err := s.UpdateXMLTVSource(ctx, got); err != nil {
		t.Fatalf("update: %v", err)
	}
	again, err := s.GetXMLTVSource(ctx, got.ID)
	if err != nil {
		t.Fatalf("get post-update: %v", err)
	}
	if again.Gzip || again.RefreshInterval != 90*time.Minute {
		t.Fatalf("update did not stick: %+v", again)
	}

	when := time.Now().UTC().Truncate(time.Second)
	if err := s.MarkXMLTVStatus(ctx, got.ID, "200 OK", "etag-xyz", "Tue, 02 Feb 2024 00:00:00 GMT", when); err != nil {
		t.Fatalf("mark status: %v", err)
	}
	statusGot, err := s.GetXMLTVSource(ctx, got.ID)
	if err != nil {
		t.Fatalf("get post-mark: %v", err)
	}
	if statusGot.LastStatus != "200 OK" || statusGot.ETag != "etag-xyz" {
		t.Fatalf("mark fields missing: %+v", statusGot)
	}
	if statusGot.LastRefreshedAt == nil || !statusGot.LastRefreshedAt.Equal(when) {
		t.Fatalf("last_refreshed_at = %v, want %v", statusGot.LastRefreshedAt, when)
	}
	if statusGot.RefreshInterval != 90*time.Minute || statusGot.Gzip {
		t.Fatalf("mark clobbered unrelated columns: %+v", statusGot)
	}

	list, err := s.ListXMLTVSources(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("list len = %d, want 1", len(list))
	}

	if err := s.DeleteXMLTVSource(ctx, got.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.GetXMLTVSource(ctx, got.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("post-delete get = %v, want ErrNotFound", err)
	}
}
