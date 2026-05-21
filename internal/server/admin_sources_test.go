package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ContinuumApp/continuum-plugin-livetv/internal/server"
	"github.com/ContinuumApp/continuum-plugin-livetv/internal/store"
	"github.com/ContinuumApp/continuum-plugin-livetv/internal/streamproxy"
	"github.com/ContinuumApp/continuum-plugin-livetv/internal/testutil"
)

// fakeAdminWorker is the admin-test analogue of scheduler's fakeWorker: it
// records every RefreshOne id so we can assert the admin handler dispatched
// to the worker (and not e.g. invoked the wrong type).
type fakeAdminWorker struct {
	mu   sync.Mutex
	ids  []string
	wait chan string
	err  error
}

func newFakeAdminWorker() *fakeAdminWorker {
	return &fakeAdminWorker{wait: make(chan string, 4)}
}

func (f *fakeAdminWorker) RefreshOne(_ context.Context, id string) error {
	f.mu.Lock()
	f.ids = append(f.ids, id)
	f.mu.Unlock()
	// Non-blocking send: tests that don't drain wait shouldn't hang the
	// goroutine on the second call.
	select {
	case f.wait <- id:
	default:
	}
	return f.err
}

func (f *fakeAdminWorker) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.ids))
	copy(out, f.ids)
	return out
}

// newAdminTestServer wires a *server.Server with the supplied pool and test
// double workers. Mirrors newTestServer but adds admin-specific plumbing.
func newAdminTestServer(pool *pgxpool.Pool, m3u, xmltv server.RefreshWorker) *server.Server {
	st := store.New(pool)
	return &server.Server{
		Store:       st,
		Settings:    streamproxy.StaticSettings{PerUser: 3, PerChannel: 5, GuideWindow: 24 * time.Hour},
		Logger:      hclog.NewNullLogger(),
		M3UWorker:   m3u,
		XMLTVWorker: xmltv,
	}
}

// adminReq builds an admin-authenticated request. body may be nil.
func adminReq(method, path string, body io.Reader) *http.Request {
	r := httptest.NewRequest(method, path, body)
	r.Header.Set("X-Continuum-User-Id", "admin-a")
	r.Header.Set("X-Continuum-Admin", "true")
	return r
}

// jsonBody marshals v into a fresh bytes.Buffer; tests use it to build
// request bodies inline without ceremony.
func jsonBody(t *testing.T, v any) *bytes.Buffer {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return bytes.NewBuffer(b)
}

func TestAdminM3USources_CRUDPlusRefresh(t *testing.T) {
	pool := testutil.StartPG(t)
	m3u := newFakeAdminWorker()
	srv := newAdminTestServer(pool, m3u, newFakeAdminWorker())

	// POST → 201 + body with id.
	createBody := map[string]any{
		"name":             "Provider A",
		"url":              "https://provider.example/playlist.m3u",
		"http_headers":     map[string]string{"User-Agent": "livetv/test"},
		"enabled":          true,
		"refresh_interval": "6h",
	}
	rr := runRequest(srv, adminReq(http.MethodPost, "/api/v1/livetv/admin/sources/m3u/", jsonBody(t, createBody)))
	if rr.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", rr.Code, rr.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("created id empty: %v", created)
	}
	if created["refresh_interval"] != "6h0m0s" {
		t.Errorf("refresh_interval = %v, want 6h0m0s", created["refresh_interval"])
	}

	// GET list contains it.
	rr = runRequest(srv, adminReq(http.MethodGet, "/api/v1/livetv/admin/sources/m3u/", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("list status = %d", rr.Code)
	}
	var list struct {
		Data []map[string]any `json:"data"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &list)
	if len(list.Data) != 1 || list.Data[0]["id"].(string) != id {
		t.Fatalf("list = %+v, want one row with id %s", list.Data, id)
	}

	// GET /{id} returns it.
	rr = runRequest(srv, adminReq(http.MethodGet, "/api/v1/livetv/admin/sources/m3u/"+id, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("get status = %d body=%s", rr.Code, rr.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	if got["name"] != "Provider A" {
		t.Errorf("name = %v", got["name"])
	}

	// PUT updates a field, GET reflects the change.
	updateBody := map[string]any{
		"name":             "Provider Renamed",
		"url":              "https://provider.example/playlist.m3u",
		"http_headers":     map[string]string{},
		"enabled":          false,
		"refresh_interval": "12h",
	}
	rr = runRequest(srv, adminReq(http.MethodPut, "/api/v1/livetv/admin/sources/m3u/"+id, jsonBody(t, updateBody)))
	if rr.Code != http.StatusOK {
		t.Fatalf("update status = %d body=%s", rr.Code, rr.Body.String())
	}
	rr = runRequest(srv, adminReq(http.MethodGet, "/api/v1/livetv/admin/sources/m3u/"+id, nil))
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	if got["name"] != "Provider Renamed" || got["enabled"] != false || got["refresh_interval"] != "12h0m0s" {
		t.Errorf("post-update body = %+v", got)
	}

	// POST /{id}/refresh → 202, fake worker records the call.
	rr = runRequest(srv, adminReq(http.MethodPost, "/api/v1/livetv/admin/sources/m3u/"+id+"/refresh", nil))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("refresh status = %d body=%s", rr.Code, rr.Body.String())
	}
	select {
	case got := <-m3u.wait:
		if got != id {
			t.Errorf("worker called with %s, want %s", got, id)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("worker.RefreshOne not called within timeout; calls=%v", m3u.calls())
	}

	// DELETE then GET → 404.
	rr = runRequest(srv, adminReq(http.MethodDelete, "/api/v1/livetv/admin/sources/m3u/"+id, nil))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d", rr.Code)
	}
	rr = runRequest(srv, adminReq(http.MethodGet, "/api/v1/livetv/admin/sources/m3u/"+id, nil))
	if rr.Code != http.StatusNotFound {
		t.Errorf("get after delete = %d, want 404", rr.Code)
	}
}

func TestAdminM3USources_InvalidDurationRejected(t *testing.T) {
	pool := testutil.StartPG(t)
	srv := newAdminTestServer(pool, newFakeAdminWorker(), newFakeAdminWorker())

	body := map[string]any{
		"name":             "X",
		"url":              "https://x",
		"enabled":          true,
		"refresh_interval": "not-a-duration",
	}
	rr := runRequest(srv, adminReq(http.MethodPost, "/api/v1/livetv/admin/sources/m3u/", jsonBody(t, body)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestAdminM3USources_RequiresAdminHeader(t *testing.T) {
	pool := testutil.StartPG(t)
	srv := newAdminTestServer(pool, newFakeAdminWorker(), newFakeAdminWorker())

	// User-only request (no admin header) must be 403.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/livetv/admin/sources/m3u/", nil)
	req.Header.Set("X-Continuum-User-Id", "user-a")
	rr := runRequest(srv, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

func TestAdminXMLTVSources_CRUDPlusRefresh(t *testing.T) {
	pool := testutil.StartPG(t)
	xmltv := newFakeAdminWorker()
	srv := newAdminTestServer(pool, newFakeAdminWorker(), xmltv)

	createBody := map[string]any{
		"name":             "EPG Provider",
		"url":              "https://epg.example/xmltv.xml.gz",
		"http_headers":     map[string]string{},
		"enabled":          true,
		"refresh_interval": "3h",
		"gzip":             true,
	}
	rr := runRequest(srv, adminReq(http.MethodPost, "/api/v1/livetv/admin/sources/xmltv/", jsonBody(t, createBody)))
	if rr.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", rr.Code, rr.Body.String())
	}
	var created map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &created)
	id := created["id"].(string)
	if id == "" {
		t.Fatalf("empty id")
	}
	if created["gzip"] != true {
		t.Errorf("gzip = %v, want true", created["gzip"])
	}

	// PUT update — flip gzip off, change name.
	updateBody := map[string]any{
		"name":             "EPG Provider v2",
		"url":              "https://epg.example/xmltv.xml",
		"http_headers":     map[string]string{},
		"enabled":          true,
		"refresh_interval": "1h",
		"gzip":             false,
	}
	rr = runRequest(srv, adminReq(http.MethodPut, "/api/v1/livetv/admin/sources/xmltv/"+id, jsonBody(t, updateBody)))
	if rr.Code != http.StatusOK {
		t.Fatalf("update status = %d body=%s", rr.Code, rr.Body.String())
	}
	rr = runRequest(srv, adminReq(http.MethodGet, "/api/v1/livetv/admin/sources/xmltv/"+id, nil))
	var got map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	if got["name"] != "EPG Provider v2" || got["gzip"] != false {
		t.Errorf("post-update body = %+v", got)
	}

	// Refresh dispatches to the xmltv worker.
	rr = runRequest(srv, adminReq(http.MethodPost, "/api/v1/livetv/admin/sources/xmltv/"+id+"/refresh", nil))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("refresh status = %d", rr.Code)
	}
	select {
	case gotID := <-xmltv.wait:
		if gotID != id {
			t.Errorf("xmltv worker called with %s, want %s", gotID, id)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("xmltv worker not called; calls=%v", xmltv.calls())
	}

	// DELETE then GET → 404.
	rr = runRequest(srv, adminReq(http.MethodDelete, "/api/v1/livetv/admin/sources/xmltv/"+id, nil))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d", rr.Code)
	}
	rr = runRequest(srv, adminReq(http.MethodGet, "/api/v1/livetv/admin/sources/xmltv/"+id, nil))
	if rr.Code != http.StatusNotFound {
		t.Errorf("get after delete = %d, want 404", rr.Code)
	}
}

func TestAdminXMLTVSources_InvalidDurationRejected(t *testing.T) {
	pool := testutil.StartPG(t)
	srv := newAdminTestServer(pool, newFakeAdminWorker(), newFakeAdminWorker())

	body := map[string]any{
		"name":             "X",
		"url":              "https://x",
		"enabled":          true,
		"refresh_interval": "garbage",
		"gzip":             false,
	}
	rr := runRequest(srv, adminReq(http.MethodPost, "/api/v1/livetv/admin/sources/xmltv/", jsonBody(t, body)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}
