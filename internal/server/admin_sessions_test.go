package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/RXWatcher/continuum-plugin-livetv/internal/store"
	"github.com/RXWatcher/continuum-plugin-livetv/internal/testutil"
)

// seedSession inserts an active stream session row for the given (user,
// channel) pair and returns the assigned id. Tests use it to populate the
// list endpoint without spinning up the full stream-proxy plumbing.
func seedSession(t *testing.T, ctx context.Context, s *store.Store, userID, channelID string) string {
	t.Helper()
	sess, err := s.CreateSession(ctx, store.Session{
		UserID:        userID,
		ChannelID:     channelID,
		ScopedGrantID: "grant-" + userID,
		SessionSecret: []byte("secret"),
	})
	if err != nil {
		t.Fatalf("seed session: %v", err)
	}
	return sess.ID
}

func TestAdminSessions_ListThenKill(t *testing.T) {
	pool := testutil.StartPG(t)
	srv := newAdminTestServer(pool, newFakeAdminWorker(), newFakeAdminWorker())
	st := store.New(pool)
	ctx := context.Background()

	srcID := seedSource(t, ctx, st)
	chA := seedChannel(t, ctx, st, srcID, "A", "G", "1")
	chB := seedChannel(t, ctx, st, srcID, "B", "G", "2")

	sessA := seedSession(t, ctx, st, "user-a", chA)
	sessB := seedSession(t, ctx, st, "user-a", chB)

	// GET /admin/sessions returns both.
	rr := runRequest(srv, adminReq(http.MethodGet, "/api/v1/livetv/admin/sessions", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var env struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(env.Data) != 2 {
		t.Fatalf("rows = %d, want 2", len(env.Data))
	}

	// Kill sessA.
	rr = runRequest(srv, adminReq(http.MethodPost, "/api/v1/livetv/admin/sessions/"+sessA+"/kill", nil))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("kill status = %d body=%s", rr.Code, rr.Body.String())
	}

	// List now shows only the survivor.
	rr = runRequest(srv, adminReq(http.MethodGet, "/api/v1/livetv/admin/sessions", nil))
	_ = json.Unmarshal(rr.Body.Bytes(), &env)
	if len(env.Data) != 1 || env.Data[0]["id"].(string) != sessB {
		t.Fatalf("after kill = %+v, want only %s", env.Data, sessB)
	}

	// Killed session's GetSession reports the admin_kill end_reason.
	killed, err := st.GetSession(ctx, sessA)
	if err != nil {
		t.Fatalf("get killed: %v", err)
	}
	if killed.EndReason != "admin_kill" {
		t.Errorf("end_reason = %q, want admin_kill", killed.EndReason)
	}
	if killed.EndedAt == nil {
		t.Errorf("ended_at not set")
	}
}

func TestAdminSessions_KillIdempotent(t *testing.T) {
	pool := testutil.StartPG(t)
	srv := newAdminTestServer(pool, newFakeAdminWorker(), newFakeAdminWorker())
	st := store.New(pool)
	ctx := context.Background()

	srcID := seedSource(t, ctx, st)
	chA := seedChannel(t, ctx, st, srcID, "A", "G", "1")
	sessID := seedSession(t, ctx, st, "user-a", chA)

	// First kill ends it.
	rr := runRequest(srv, adminReq(http.MethodPost, "/api/v1/livetv/admin/sessions/"+sessID+"/kill", nil))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("first kill status = %d", rr.Code)
	}
	// Second kill is a no-op (no error, still 204).
	rr = runRequest(srv, adminReq(http.MethodPost, "/api/v1/livetv/admin/sessions/"+sessID+"/kill", nil))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("second kill status = %d", rr.Code)
	}
}

func TestAdminSessions_KillUnknownIDIsNoOp(t *testing.T) {
	pool := testutil.StartPG(t)
	srv := newAdminTestServer(pool, newFakeAdminWorker(), newFakeAdminWorker())

	// Unknown id: store treats this as a no-op (UPDATE matches 0 rows). The
	// admin handler picks 204 rather than 404 so stale UI state doesn't
	// flood the operator with errors.
	rr := runRequest(srv, adminReq(http.MethodPost, "/api/v1/livetv/admin/sessions/nope/kill", nil))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rr.Code)
	}
}
