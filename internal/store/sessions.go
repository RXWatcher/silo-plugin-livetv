package store

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/jackc/pgx/v5"
)

// Session mirrors the stream_sessions table. ClientIP is the textual form
// of the inet column; an empty string here means NULL in the database.
type Session struct {
	ID            string
	UserID        string
	ChannelID     string
	ScopedGrantID string
	SessionSecret []byte
	StartedAt     time.Time
	LastByteAt    time.Time
	BytesStreamed int64
	ClientIP      string
	UserAgent     string
	EndedAt       *time.Time
	EndReason     string
}

// parseClientIP turns a possibly-empty text address into an `any` suitable
// for an `inet` parameter. Empty or unparseable input becomes nil so the
// column stores SQL NULL.
func parseClientIP(s string) any {
	if s == "" {
		return nil
	}
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return nil
	}
	return addr
}

// scanClientIP reads an inet column into a string. NULL becomes "".
func scanClientIP(addr *netip.Addr) string {
	if addr == nil || !addr.IsValid() {
		return ""
	}
	return addr.String()
}

// CreateSession inserts a stream session. Assigns a ULID id if empty and
// populates StartedAt / LastByteAt from now() when zero.
func (s *Store) CreateSession(ctx context.Context, sess Session) (Session, error) {
	if sess.ID == "" {
		sess.ID = newULID()
	}
	now := time.Now().UTC()
	if sess.StartedAt.IsZero() {
		sess.StartedAt = now
	}
	if sess.LastByteAt.IsZero() {
		sess.LastByteAt = sess.StartedAt
	}
	if _, err := s.Pool.Exec(ctx, `
		INSERT INTO stream_sessions (
			id, user_id, channel_id, scoped_grant_id, session_secret,
			started_at, last_byte_at, bytes_streamed,
			client_ip, user_agent, ended_at, end_reason
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
	`,
		sess.ID, sess.UserID, sess.ChannelID, sess.ScopedGrantID, sess.SessionSecret,
		sess.StartedAt, sess.LastByteAt, sess.BytesStreamed,
		parseClientIP(sess.ClientIP), sess.UserAgent, sess.EndedAt, sess.EndReason,
	); err != nil {
		return Session{}, fmt.Errorf("create session: %w", err)
	}
	return s.GetSession(ctx, sess.ID)
}

// UpdateSessionLastByte advances the activity stamp and adds bytes streamed.
// Returns ErrNotFound when the session id is unknown.
func (s *Store) UpdateSessionLastByte(ctx context.Context, id string, lastByte time.Time, bytes int64) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE stream_sessions
		SET last_byte_at = $2, bytes_streamed = bytes_streamed + $3
		WHERE id = $1 AND ended_at IS NULL
	`, id, lastByte, bytes)
	if err != nil {
		return fmt.Errorf("update last byte: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// EndSession marks the session ended with the supplied reason. Idempotent:
// re-ending a session is a no-op (the first ended_at sticks).
func (s *Store) EndSession(ctx context.Context, id, reason string) error {
	_, err := s.Pool.Exec(ctx, `
		UPDATE stream_sessions
		SET ended_at = now(), end_reason = $2
		WHERE id = $1 AND ended_at IS NULL
	`, id, reason)
	if err != nil {
		return fmt.Errorf("end session: %w", err)
	}
	return nil
}

// GetSession returns a session by id, or ErrNotFound.
func (s *Store) GetSession(ctx context.Context, id string) (Session, error) {
	row := s.Pool.QueryRow(ctx, `
		SELECT id, user_id, channel_id, scoped_grant_id, session_secret,
		       started_at, last_byte_at, bytes_streamed,
		       client_ip, user_agent, ended_at, end_reason
		FROM stream_sessions WHERE id = $1
	`, id)
	sess, err := scanSession(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Session{}, ErrNotFound
		}
		return Session{}, err
	}
	return sess, nil
}

// CountActiveByUser returns the number of sessions for userID where
// ended_at IS NULL.
func (s *Store) CountActiveByUser(ctx context.Context, userID string) (int, error) {
	var n int
	if err := s.Pool.QueryRow(ctx, `
		SELECT count(*) FROM stream_sessions
		WHERE user_id = $1 AND ended_at IS NULL
	`, userID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count active by user: %w", err)
	}
	return n, nil
}

// CountActiveByChannel returns the number of sessions for channelID where
// ended_at IS NULL.
func (s *Store) CountActiveByChannel(ctx context.Context, channelID string) (int, error) {
	var n int
	if err := s.Pool.QueryRow(ctx, `
		SELECT count(*) FROM stream_sessions
		WHERE channel_id = $1 AND ended_at IS NULL
	`, channelID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count active by channel: %w", err)
	}
	return n, nil
}

// ListActiveSessions returns every session with ended_at IS NULL, ordered
// by started_at ASC for stable display.
func (s *Store) ListActiveSessions(ctx context.Context) ([]Session, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id, user_id, channel_id, scoped_grant_id, session_secret,
		       started_at, last_byte_at, bytes_streamed,
		       client_ip, user_agent, ended_at, end_reason
		FROM stream_sessions
		WHERE ended_at IS NULL
		ORDER BY started_at ASC, id ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list active sessions: %w", err)
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

// ReapIdle ends every active session whose last_byte_at predates cutoff.
// Returns the ids of the affected rows so callers can log / propagate.
func (s *Store) ReapIdle(ctx context.Context, cutoff time.Time) ([]string, error) {
	rows, err := s.Pool.Query(ctx, `
		UPDATE stream_sessions
		SET ended_at = now(), end_reason = 'idle'
		WHERE ended_at IS NULL AND last_byte_at < $1
		RETURNING id
	`, cutoff)
	if err != nil {
		return nil, fmt.Errorf("reap idle: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan reaped id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// scanSession scans a row produced by the projections above. client_ip is a
// nullable inet column; we surface it as the textual form (or "" on NULL).
func scanSession(row pgx.Row) (Session, error) {
	var (
		sess Session
		ip   *netip.Addr
	)
	if err := row.Scan(
		&sess.ID, &sess.UserID, &sess.ChannelID, &sess.ScopedGrantID, &sess.SessionSecret,
		&sess.StartedAt, &sess.LastByteAt, &sess.BytesStreamed,
		&ip, &sess.UserAgent, &sess.EndedAt, &sess.EndReason,
	); err != nil {
		return Session{}, fmt.Errorf("scan session: %w", err)
	}
	sess.ClientIP = scanClientIP(ip)
	return sess, nil
}
