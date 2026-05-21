package store_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ContinuumApp/continuum-plugin-livetv/internal/store"
	"github.com/ContinuumApp/continuum-plugin-livetv/internal/testutil"
)

func TestCreateAndGetSession(t *testing.T) {
	pool := testutil.StartPG(t)
	s := store.New(pool)
	ctx := context.Background()
	ids := seedChannels(t, ctx, s, "live1")

	secret := []byte{0xde, 0xad, 0xbe, 0xef, 0x00, 0x01}
	in := store.Session{
		UserID:        "u1",
		ChannelID:     ids[0],
		ScopedGrantID: "grant-1",
		SessionSecret: secret,
		ClientIP:      "192.0.2.10",
		UserAgent:     "TestClient/1.0",
	}
	created, err := s.CreateSession(ctx, in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ID == "" {
		t.Fatalf("expected an assigned id")
	}
	if !bytes.Equal(created.SessionSecret, secret) {
		t.Fatalf("session_secret round-trip: %x, want %x", created.SessionSecret, secret)
	}
	if created.ClientIP != "192.0.2.10" {
		t.Fatalf("client_ip = %q, want 192.0.2.10", created.ClientIP)
	}
	if created.UserAgent != "TestClient/1.0" {
		t.Fatalf("user_agent = %q", created.UserAgent)
	}
	if created.StartedAt.IsZero() || created.LastByteAt.IsZero() {
		t.Fatalf("timestamps not populated: %+v", created)
	}

	got, err := s.GetSession(ctx, created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !bytes.Equal(got.SessionSecret, secret) {
		t.Fatalf("re-fetched secret: %x", got.SessionSecret)
	}
}

func TestCreateSessionEmptyClientIPStoresNull(t *testing.T) {
	pool := testutil.StartPG(t)
	s := store.New(pool)
	ctx := context.Background()
	ids := seedChannels(t, ctx, s, "x")

	sess, err := s.CreateSession(ctx, store.Session{
		UserID: "u1", ChannelID: ids[0], ScopedGrantID: "g",
		SessionSecret: []byte("s"), ClientIP: "", UserAgent: "ua",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if sess.ClientIP != "" {
		t.Fatalf("client_ip = %q, want empty", sess.ClientIP)
	}
	var nullCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM stream_sessions WHERE id=$1 AND client_ip IS NULL`, sess.ID).Scan(&nullCount); err != nil {
		t.Fatalf("count: %v", err)
	}
	if nullCount != 1 {
		t.Fatalf("client_ip not NULL in db: %d", nullCount)
	}

	// Also: garbage input falls through to NULL rather than erroring.
	sess2, err := s.CreateSession(ctx, store.Session{
		UserID: "u1", ChannelID: ids[0], ScopedGrantID: "g2",
		SessionSecret: []byte("s"), ClientIP: "not-an-ip", UserAgent: "ua",
	})
	if err != nil {
		t.Fatalf("create with garbage ip: %v", err)
	}
	if sess2.ClientIP != "" {
		t.Fatalf("garbage ip = %q, want empty", sess2.ClientIP)
	}
}

func TestSessionCountsAndEnd(t *testing.T) {
	pool := testutil.StartPG(t)
	s := store.New(pool)
	ctx := context.Background()
	ids := seedChannels(t, ctx, s, "a", "b")

	a1, err := s.CreateSession(ctx, store.Session{
		UserID: "u1", ChannelID: ids[0], ScopedGrantID: "g",
		SessionSecret: []byte("s"), UserAgent: "ua",
	})
	if err != nil {
		t.Fatalf("create a1: %v", err)
	}
	if _, err := s.CreateSession(ctx, store.Session{
		UserID: "u1", ChannelID: ids[1], ScopedGrantID: "g2",
		SessionSecret: []byte("s"), UserAgent: "ua",
	}); err != nil {
		t.Fatalf("create a2: %v", err)
	}
	if _, err := s.CreateSession(ctx, store.Session{
		UserID: "u2", ChannelID: ids[0], ScopedGrantID: "g3",
		SessionSecret: []byte("s"), UserAgent: "ua",
	}); err != nil {
		t.Fatalf("create b1: %v", err)
	}

	n, err := s.CountActiveByUser(ctx, "u1")
	if err != nil {
		t.Fatalf("count u1: %v", err)
	}
	if n != 2 {
		t.Fatalf("u1 active = %d, want 2", n)
	}
	n, err = s.CountActiveByChannel(ctx, ids[0])
	if err != nil {
		t.Fatalf("count ch: %v", err)
	}
	if n != 2 {
		t.Fatalf("ch active = %d, want 2", n)
	}
	active, err := s.ListActiveSessions(ctx)
	if err != nil {
		t.Fatalf("list active: %v", err)
	}
	if len(active) != 3 {
		t.Fatalf("all active = %d, want 3", len(active))
	}

	// End all of u1's sessions.
	if err := s.EndSession(ctx, a1.ID, "client-disconnect"); err != nil {
		t.Fatalf("end a1: %v", err)
	}
	// Re-ending is a no-op.
	if err := s.EndSession(ctx, a1.ID, "different-reason"); err != nil {
		t.Fatalf("re-end: %v", err)
	}
	got, err := s.GetSession(ctx, a1.ID)
	if err != nil {
		t.Fatalf("get a1: %v", err)
	}
	if got.EndedAt == nil {
		t.Fatalf("ended_at not set")
	}
	if got.EndReason != "client-disconnect" {
		t.Fatalf("end_reason = %q, want client-disconnect", got.EndReason)
	}

	// End the other u1 session too and verify u1 count drops to 0.
	for _, sess := range active {
		if sess.UserID == "u1" {
			_ = s.EndSession(ctx, sess.ID, "test")
		}
	}
	n, _ = s.CountActiveByUser(ctx, "u1")
	if n != 0 {
		t.Fatalf("u1 active after ends = %d, want 0", n)
	}
}

func TestUpdateSessionLastByte(t *testing.T) {
	pool := testutil.StartPG(t)
	s := store.New(pool)
	ctx := context.Background()
	ids := seedChannels(t, ctx, s, "x")
	sess, _ := s.CreateSession(ctx, store.Session{
		UserID: "u1", ChannelID: ids[0], ScopedGrantID: "g",
		SessionSecret: []byte("s"), UserAgent: "ua",
	})
	stamp := time.Now().UTC().Add(10 * time.Second).Truncate(time.Microsecond)
	if err := s.UpdateSessionLastByte(ctx, sess.ID, stamp, 1024); err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := s.UpdateSessionLastByte(ctx, sess.ID, stamp.Add(time.Second), 512); err != nil {
		t.Fatalf("update 2: %v", err)
	}
	got, _ := s.GetSession(ctx, sess.ID)
	if got.BytesStreamed != 1536 {
		t.Fatalf("bytes = %d, want 1536", got.BytesStreamed)
	}
	// Missing id -> ErrNotFound.
	if err := s.UpdateSessionLastByte(ctx, "no-such-session", stamp, 1); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestReapIdle(t *testing.T) {
	pool := testutil.StartPG(t)
	s := store.New(pool)
	ctx := context.Background()
	ids := seedChannels(t, ctx, s, "x")
	idle, _ := s.CreateSession(ctx, store.Session{
		UserID: "u1", ChannelID: ids[0], ScopedGrantID: "g",
		SessionSecret: []byte("s"), UserAgent: "ua",
	})
	fresh, _ := s.CreateSession(ctx, store.Session{
		UserID: "u2", ChannelID: ids[0], ScopedGrantID: "g",
		SessionSecret: []byte("s"), UserAgent: "ua",
	})

	// Push idle's last_byte_at far into the past.
	oldStamp := time.Now().UTC().Add(-10 * time.Minute)
	if _, err := pool.Exec(ctx, `UPDATE stream_sessions SET last_byte_at = $1 WHERE id = $2`, oldStamp, idle.ID); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	cutoff := time.Now().UTC().Add(-5 * time.Minute)
	reaped, err := s.ReapIdle(ctx, cutoff)
	if err != nil {
		t.Fatalf("reap: %v", err)
	}
	if len(reaped) != 1 || reaped[0] != idle.ID {
		t.Fatalf("reaped = %+v, want [%s]", reaped, idle.ID)
	}

	got, _ := s.GetSession(ctx, idle.ID)
	if got.EndedAt == nil {
		t.Fatalf("idle session not ended")
	}
	if got.EndReason != "idle" {
		t.Fatalf("end_reason = %q, want idle", got.EndReason)
	}
	// Fresh session untouched.
	freshGot, _ := s.GetSession(ctx, fresh.ID)
	if freshGot.EndedAt != nil {
		t.Fatalf("fresh session was incorrectly reaped")
	}

	// Second call returns no rows.
	reaped2, err := s.ReapIdle(ctx, cutoff)
	if err != nil {
		t.Fatalf("reap2: %v", err)
	}
	if len(reaped2) != 0 {
		t.Fatalf("second reap returned %+v, want empty", reaped2)
	}
}
