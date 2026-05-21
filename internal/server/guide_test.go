package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/ContinuumApp/continuum-plugin-livetv/internal/server"
	"github.com/ContinuumApp/continuum-plugin-livetv/internal/store"
	"github.com/ContinuumApp/continuum-plugin-livetv/internal/streamproxy"
	"github.com/ContinuumApp/continuum-plugin-livetv/internal/testutil"
)

func TestGroups_ListReturnsDistinctEffectiveGroups(t *testing.T) {
	pool := testutil.StartPG(t)
	srv := newTestServer(pool)
	st := store.New(pool)
	ctx := context.Background()

	srcID := seedSource(t, ctx, st)
	_ = seedChannel(t, ctx, st, srcID, "alpha", "News", "100")
	_ = seedChannel(t, ctx, st, srcID, "beta", "Sport", "200")
	_ = seedChannel(t, ctx, st, srcID, "gamma", "News", "300")

	rr := runRequest(srv, authedReq(http.MethodGet, "/api/v1/livetv/groups", "u", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var env struct {
		Data []string `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(env.Data) != 2 {
		t.Fatalf("groups = %+v, want 2 distinct", env.Data)
	}
}

func TestGuide_GroupsByChannelIDAndRespectsWindow(t *testing.T) {
	pool := testutil.StartPG(t)
	srv := newTestServer(pool)
	st := store.New(pool)
	ctx := context.Background()

	srcID := seedSource(t, ctx, st)
	chA := seedChannel(t, ctx, st, srcID, "A", "G", "1")
	chB := seedChannel(t, ctx, st, srcID, "B", "G", "2")
	if err := st.AddEPGKey(ctx, chA, "xmltv.a", true); err != nil {
		t.Fatalf("epg link a: %v", err)
	}
	if err := st.AddEPGKey(ctx, chB, "xmltv.b", true); err != nil {
		t.Fatalf("epg link b: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	// 3 programs each across a 6h window; we'll request a 4h slice.
	chAProgs := []store.Program{
		{Start: now.Add(30 * time.Minute), Stop: now.Add(60 * time.Minute), Title: "A1"},
		{Start: now.Add(90 * time.Minute), Stop: now.Add(2 * time.Hour), Title: "A2"},
		{Start: now.Add(5 * time.Hour), Stop: now.Add(6 * time.Hour), Title: "A3-outside"},
	}
	chBProgs := []store.Program{
		{Start: now.Add(1 * time.Hour), Stop: now.Add(2 * time.Hour), Title: "B1"},
		{Start: now.Add(2 * time.Hour), Stop: now.Add(3 * time.Hour), Title: "B2"},
		{Start: now.Add(5 * time.Hour), Stop: now.Add(6 * time.Hour), Title: "B3-outside"},
	}
	if err := st.ReplaceFutureForChannel(ctx, "xmltv.a", chAProgs); err != nil {
		t.Fatalf("seed a: %v", err)
	}
	if err := st.ReplaceFutureForChannel(ctx, "xmltv.b", chBProgs); err != nil {
		t.Fatalf("seed b: %v", err)
	}

	// Request a 4h window starting now.
	start := now
	end := now.Add(4 * time.Hour)
	path := "/api/v1/livetv/guide?start=" + url.QueryEscape(start.Format(time.RFC3339)) + "&end=" + url.QueryEscape(end.Format(time.RFC3339))
	rr := runRequest(srv, authedReq(http.MethodGet, path, "u", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}

	var out struct {
		Data   map[string][]map[string]any `json:"data"`
		Window struct {
			Start time.Time `json:"start"`
			End   time.Time `json:"end"`
		} `json:"window"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v body=%s", err, rr.Body.String())
	}

	// Keys must be channel ids (NOT xmltv ids).
	if _, ok := out.Data[chA]; !ok {
		t.Fatalf("data missing chA (%s); keys=%v", chA, mapKeys(out.Data))
	}
	if _, ok := out.Data[chB]; !ok {
		t.Fatalf("data missing chB (%s); keys=%v", chB, mapKeys(out.Data))
	}
	if _, bad := out.Data["xmltv.a"]; bad {
		t.Fatalf("guide keyed by xmltv id; want channel id")
	}
	if len(out.Data[chA]) != 2 {
		t.Errorf("chA rows = %d, want 2 inside window", len(out.Data[chA]))
	}
	if len(out.Data[chB]) != 2 {
		t.Errorf("chB rows = %d, want 2 inside window", len(out.Data[chB]))
	}

	if !out.Window.Start.Equal(start) {
		t.Errorf("window.start = %v, want %v", out.Window.Start, start)
	}
	if !out.Window.End.Equal(end) {
		t.Errorf("window.end = %v, want %v", out.Window.End, end)
	}
}

func TestGuide_CapsOverlongWindow(t *testing.T) {
	pool := testutil.StartPG(t)
	// Custom server with a tight cap so the request gets clipped.
	st := store.New(pool)
	srv := &server.Server{
		Store:    st,
		Settings: streamproxy.StaticSettings{GuideWindow: 2 * time.Hour},
	}

	start := time.Now().UTC().Truncate(time.Second)
	end := start.Add(48 * time.Hour)
	path := "/api/v1/livetv/guide?start=" + url.QueryEscape(start.Format(time.RFC3339)) + "&end=" + url.QueryEscape(end.Format(time.RFC3339))

	rr := runRequest(srv, authedReq(http.MethodGet, path, "u", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var out struct {
		Window struct {
			Start time.Time `json:"start"`
			End   time.Time `json:"end"`
		} `json:"window"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	if got := out.Window.End.Sub(out.Window.Start); got != 2*time.Hour {
		t.Fatalf("window length = %v, want 2h (cap)", got)
	}
}

func TestGuide_DefaultsWhenStartAndEndOmitted(t *testing.T) {
	pool := testutil.StartPG(t)
	srv := newTestServer(pool)

	rr := runRequest(srv, authedReq(http.MethodGet, "/api/v1/livetv/guide", "u", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var out struct {
		Window struct {
			Start time.Time `json:"start"`
			End   time.Time `json:"end"`
		} `json:"window"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	if got := out.Window.End.Sub(out.Window.Start); got != 4*time.Hour {
		t.Errorf("default window length = %v, want 4h", got)
	}
}

func mapKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
