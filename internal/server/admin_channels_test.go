package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/RXWatcher/continuum-plugin-livetv/internal/store"
	"github.com/RXWatcher/continuum-plugin-livetv/internal/testutil"
)

func TestAdminChannels_ListReturnsTriplet(t *testing.T) {
	pool := testutil.StartPG(t)
	srv := newAdminTestServer(pool, newFakeAdminWorker(), newFakeAdminWorker())
	st := store.New(pool)
	ctx := context.Background()

	srcID := seedSource(t, ctx, st)
	id := seedChannel(t, ctx, st, srcID, "Alpha", "News", "101")

	// Apply an override so the response shows src vs admin vs effective.
	num501 := "501"
	if err := st.AdminPatchChannel(ctx, id, store.ChannelPatch{
		ChannelNumberAdmin: store.SetChannelNumberAdmin(&num501),
	}); err != nil {
		t.Fatalf("patch: %v", err)
	}

	rr := runRequest(srv, adminReq(http.MethodGet, "/api/v1/livetv/admin/channels", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var env struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(env.Data) != 1 {
		t.Fatalf("rows = %d, want 1", len(env.Data))
	}
	row := env.Data[0]
	if row["channel_number_src"] != "101" {
		t.Errorf("src = %v, want 101", row["channel_number_src"])
	}
	if row["channel_number_admin"] != "501" {
		t.Errorf("admin = %v, want 501", row["channel_number_admin"])
	}
	if row["channel_number_effective"] != "501" {
		t.Errorf("eff = %v, want 501", row["channel_number_effective"])
	}
}

func TestAdminChannels_PatchSetThenClear(t *testing.T) {
	pool := testutil.StartPG(t)
	srv := newAdminTestServer(pool, newFakeAdminWorker(), newFakeAdminWorker())
	st := store.New(pool)
	ctx := context.Background()

	srcID := seedSource(t, ctx, st)
	id := seedChannel(t, ctx, st, srcID, "Alpha", "News", "101")

	// PATCH set the override to "501".
	setBody := map[string]any{
		"channel_number_admin": map[string]any{"set": true, "value": "501"},
	}
	rr := runRequest(srv, adminReq(http.MethodPatch, "/api/v1/livetv/admin/channels/"+id, jsonBody(t, setBody)))
	if rr.Code != http.StatusOK {
		t.Fatalf("set status = %d body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["channel_number_admin"] != "501" || resp["channel_number_effective"] != "501" {
		t.Errorf("after set: %+v", resp)
	}

	// User-facing GET /channels reflects the effective change.
	uRR := runRequest(srv, authedReq(http.MethodGet, "/api/v1/livetv/channels/"+id, "user-a", nil))
	if uRR.Code != http.StatusOK {
		t.Fatalf("user get status = %d", uRR.Code)
	}
	var userDTO map[string]any
	_ = json.Unmarshal(uRR.Body.Bytes(), &userDTO)
	if userDTO["channel_number"] != "501" {
		t.Errorf("user channel_number = %v, want 501", userDTO["channel_number"])
	}
	// User-facing DTO must NOT expose the src/admin triplet.
	if _, ok := userDTO["channel_number_admin"]; ok {
		t.Errorf("user DTO leaked channel_number_admin: %v", userDTO)
	}
	if _, ok := userDTO["channel_number_src"]; ok {
		t.Errorf("user DTO leaked channel_number_src: %v", userDTO)
	}

	// PATCH clear: set true, value null → admin override reverts to nil.
	clearBody := map[string]any{
		"channel_number_admin": map[string]any{"set": true, "value": nil},
	}
	rr = runRequest(srv, adminReq(http.MethodPatch, "/api/v1/livetv/admin/channels/"+id, jsonBody(t, clearBody)))
	if rr.Code != http.StatusOK {
		t.Fatalf("clear status = %d body=%s", rr.Code, rr.Body.String())
	}
	// Reset resp before second decode — encoding/json into a non-empty
	// map preserves prior keys, which would mask the omitempty assertion.
	resp = nil
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if v, present := resp["channel_number_admin"]; present {
		t.Errorf("expected channel_number_admin omitted after clear, got value=%v, full=%+v", v, resp)
	}
	if resp["channel_number_effective"] != "101" {
		t.Errorf("eff after clear = %v, want 101", resp["channel_number_effective"])
	}
}

func TestAdminChannels_PatchUnchangedField(t *testing.T) {
	pool := testutil.StartPG(t)
	srv := newAdminTestServer(pool, newFakeAdminWorker(), newFakeAdminWorker())
	st := store.New(pool)
	ctx := context.Background()

	srcID := seedSource(t, ctx, st)
	id := seedChannel(t, ctx, st, srcID, "Alpha", "News", "101")

	// Pre-seed an admin number.
	num501 := "501"
	if err := st.AdminPatchChannel(ctx, id, store.ChannelPatch{
		ChannelNumberAdmin: store.SetChannelNumberAdmin(&num501),
	}); err != nil {
		t.Fatalf("seed patch: %v", err)
	}

	// PATCH body that only touches group_title_admin — number stays.
	body := map[string]any{
		"group_title_admin": map[string]any{"set": true, "value": "Custom"},
	}
	rr := runRequest(srv, adminReq(http.MethodPatch, "/api/v1/livetv/admin/channels/"+id, jsonBody(t, body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["channel_number_admin"] != "501" {
		t.Errorf("number_admin = %v, want 501 (unchanged)", resp["channel_number_admin"])
	}
	if resp["group_title_admin"] != "Custom" {
		t.Errorf("group_title_admin = %v, want Custom", resp["group_title_admin"])
	}
	if resp["group_title_effective"] != "Custom" {
		t.Errorf("group eff = %v, want Custom", resp["group_title_effective"])
	}
}

func TestAdminChannels_EPGKeysAddListRemove(t *testing.T) {
	pool := testutil.StartPG(t)
	srv := newAdminTestServer(pool, newFakeAdminWorker(), newFakeAdminWorker())
	st := store.New(pool)
	ctx := context.Background()

	srcID := seedSource(t, ctx, st)
	id := seedChannel(t, ctx, st, srcID, "Alpha", "News", "101")

	// Empty list initially.
	rr := runRequest(srv, adminReq(http.MethodGet, "/api/v1/livetv/admin/channels/"+id+"/epg-keys", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var initial struct {
		XMLTVChannelIDs []string `json:"xmltv_channel_ids"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &initial); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(initial.XMLTVChannelIDs) != 0 {
		t.Errorf("initial keys = %v, want empty", initial.XMLTVChannelIDs)
	}

	// Add one.
	rr = runRequest(srv, adminReq(http.MethodPost, "/api/v1/livetv/admin/channels/"+id+"/epg-keys",
		jsonBody(t, map[string]string{"xmltv_channel_id": "bbc.one"})))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("add status = %d body=%s", rr.Code, rr.Body.String())
	}

	// List shows it.
	rr = runRequest(srv, adminReq(http.MethodGet, "/api/v1/livetv/admin/channels/"+id+"/epg-keys", nil))
	var afterAdd struct {
		XMLTVChannelIDs []string `json:"xmltv_channel_ids"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &afterAdd)
	if len(afterAdd.XMLTVChannelIDs) != 1 || afterAdd.XMLTVChannelIDs[0] != "bbc.one" {
		t.Errorf("after add = %+v", afterAdd)
	}

	// Delete.
	rr = runRequest(srv, adminReq(http.MethodDelete, "/api/v1/livetv/admin/channels/"+id+"/epg-keys/bbc.one", nil))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d", rr.Code)
	}
	rr = runRequest(srv, adminReq(http.MethodGet, "/api/v1/livetv/admin/channels/"+id+"/epg-keys", nil))
	var afterDelete struct {
		XMLTVChannelIDs []string `json:"xmltv_channel_ids"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &afterDelete)
	if len(afterDelete.XMLTVChannelIDs) != 0 {
		t.Errorf("after delete = %v, want empty", afterDelete.XMLTVChannelIDs)
	}
	_ = st // silence unused
}

func TestAdminChannels_ListFilteredBySource(t *testing.T) {
	pool := testutil.StartPG(t)
	srv := newAdminTestServer(pool, newFakeAdminWorker(), newFakeAdminWorker())
	st := store.New(pool)
	ctx := context.Background()

	srcA := seedSource(t, ctx, st)
	srcB, err := st.CreateM3USource(ctx, store.M3USource{
		Name: "second", URL: "http://y", Enabled: true, RefreshInterval: 0,
	})
	if err != nil {
		t.Fatalf("seed second source: %v", err)
	}
	_ = seedChannel(t, ctx, st, srcA, "Alpha", "News", "1")
	bID := seedChannel(t, ctx, st, srcB.ID, "Beta", "Sport", "2")

	rr := runRequest(srv, adminReq(http.MethodGet, "/api/v1/livetv/admin/channels?source_m3u_id="+srcB.ID, nil))
	var env struct {
		Data []map[string]any `json:"data"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &env)
	if len(env.Data) != 1 || env.Data[0]["id"].(string) != bID {
		t.Errorf("filtered list = %+v, want only %s", env.Data, bID)
	}
}
