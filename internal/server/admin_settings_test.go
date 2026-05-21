package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ContinuumApp/continuum-plugin-livetv/internal/server"
	"github.com/ContinuumApp/continuum-plugin-livetv/internal/settings"
	"github.com/ContinuumApp/continuum-plugin-livetv/internal/store"
	"github.com/ContinuumApp/continuum-plugin-livetv/internal/streamproxy"
	"github.com/ContinuumApp/continuum-plugin-livetv/internal/testutil"
)

// newSettingsTestServer wires a *server.Server with a real DB-backed
// Snapshot. The Snapshot is also handed to streamproxy.Deps so the test can
// assert that PUT /admin/settings flows through to the live stream-proxy
// caps via a single Reload.
func newSettingsTestServer(t *testing.T, pool *pgxpool.Pool) (*server.Server, *settings.Snapshot) {
	t.Helper()
	st := store.New(pool)
	snap, err := settings.Load(context.Background(), st)
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	return &server.Server{
		Store:    st,
		Settings: snap,
		Stream: &streamproxy.Deps{
			Store:    st,
			Settings: snap,
		},
		Snapshot: snap,
		Logger:   hclog.NewNullLogger(),
	}, snap
}

func TestAdminSettings_GetReturnsCurrentRow(t *testing.T) {
	pool := testutil.StartPG(t)
	srv, _ := newSettingsTestServer(t, pool)

	rr := runRequest(srv, adminReq(http.MethodGet, "/api/v1/livetv/admin/settings", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Defaults from migration 0001.
	if got["per_user_stream_cap"].(float64) != 3 {
		t.Errorf("per_user_stream_cap = %v, want 3", got["per_user_stream_cap"])
	}
	if got["per_channel_default_cap"].(float64) != 5 {
		t.Errorf("per_channel_default_cap = %v, want 5", got["per_channel_default_cap"])
	}
	if got["session_idle_timeout"] != "1m0s" {
		t.Errorf("session_idle_timeout = %v, want 1m0s", got["session_idle_timeout"])
	}
}

func TestAdminSettings_PutPersistsAndReloadsSnapshot(t *testing.T) {
	pool := testutil.StartPG(t)
	srv, snap := newSettingsTestServer(t, pool)

	body := map[string]any{
		"default_m3u_refresh":     "4h",
		"default_xmltv_refresh":   "2h",
		"guide_window_cap":        "12h",
		"per_user_stream_cap":     8,
		"per_channel_default_cap": 12,
		"session_idle_timeout":    "90s",
	}
	rr := runRequest(srv, adminReq(http.MethodPut, "/api/v1/livetv/admin/settings", jsonBody(t, body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}

	// Snapshot was reloaded — the stream-proxy caps reflect the new value.
	if snap.PerUserStreamCap() != 8 {
		t.Errorf("snapshot per user = %d, want 8", snap.PerUserStreamCap())
	}
	if snap.SessionIdleTimeout() != 90*time.Second {
		t.Errorf("snapshot idle = %v, want 90s", snap.SessionIdleTimeout())
	}
	// Verify the stream-proxy sees the same value via the Settings interface
	// it received at boot.
	if srv.Stream.Settings.PerUserStreamCap() != 8 {
		t.Errorf("stream settings per user = %d, want 8", srv.Stream.Settings.PerUserStreamCap())
	}

	// GET reflects the persisted values.
	rr = runRequest(srv, adminReq(http.MethodGet, "/api/v1/livetv/admin/settings", nil))
	var got map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	if got["per_user_stream_cap"].(float64) != 8 {
		t.Errorf("GET per user = %v, want 8", got["per_user_stream_cap"])
	}
	if got["session_idle_timeout"] != "1m30s" {
		t.Errorf("GET idle = %v, want 1m30s", got["session_idle_timeout"])
	}
}

func TestAdminSettings_RejectsInvalidDuration(t *testing.T) {
	pool := testutil.StartPG(t)
	srv, _ := newSettingsTestServer(t, pool)

	body := map[string]any{
		"default_m3u_refresh":     "garbage",
		"default_xmltv_refresh":   "2h",
		"guide_window_cap":        "12h",
		"per_user_stream_cap":     5,
		"per_channel_default_cap": 5,
		"session_idle_timeout":    "60s",
	}
	rr := runRequest(srv, adminReq(http.MethodPut, "/api/v1/livetv/admin/settings", jsonBody(t, body)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestAdminSettings_RejectsNonPositiveInts(t *testing.T) {
	pool := testutil.StartPG(t)
	srv, _ := newSettingsTestServer(t, pool)

	body := map[string]any{
		"default_m3u_refresh":     "4h",
		"default_xmltv_refresh":   "2h",
		"guide_window_cap":        "12h",
		"per_user_stream_cap":     0,
		"per_channel_default_cap": 5,
		"session_idle_timeout":    "60s",
	}
	rr := runRequest(srv, adminReq(http.MethodPut, "/api/v1/livetv/admin/settings", jsonBody(t, body)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status (per_user 0) = %d body=%s", rr.Code, rr.Body.String())
	}

	body["per_user_stream_cap"] = 5
	body["per_channel_default_cap"] = -1
	rr = runRequest(srv, adminReq(http.MethodPut, "/api/v1/livetv/admin/settings", jsonBody(t, body)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status (per_channel -1) = %d body=%s", rr.Code, rr.Body.String())
	}
}
