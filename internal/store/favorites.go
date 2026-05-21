package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Favorite is one row of user_favorites for a user.
type Favorite struct {
	ChannelID string
	Position  int
}

// Recent is one row of user_recent for a user.
type Recent struct {
	ChannelID   string
	LastTunedAt time.Time
}

// ListFavorites returns the user's favorites ordered by position ASC, then
// created_at ASC for stable ties.
func (s *Store) ListFavorites(ctx context.Context, userID string) ([]Favorite, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT channel_id, position
		FROM user_favorites
		WHERE user_id = $1
		ORDER BY position ASC, created_at ASC, channel_id ASC
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("list favorites: %w", err)
	}
	defer rows.Close()
	var out []Favorite
	for rows.Next() {
		var f Favorite
		if err := rows.Scan(&f.ChannelID, &f.Position); err != nil {
			return nil, fmt.Errorf("scan favorite: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// AddFavorite inserts a (user, channel) row, computing the next position as
// (max(position)+1) for that user. Idempotent: re-adding an existing favorite
// is a no-op (position preserved).
func (s *Store) AddFavorite(ctx context.Context, userID, channelID string) error {
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO user_favorites (user_id, channel_id, position)
		SELECT $1, $2, coalesce((
			SELECT max(position) + 1 FROM user_favorites WHERE user_id = $1
		), 0)
		ON CONFLICT (user_id, channel_id) DO NOTHING
	`, userID, channelID)
	if err != nil {
		return fmt.Errorf("add favorite: %w", err)
	}
	return nil
}

// RemoveFavorite deletes a (user, channel) row. Idempotent: returns nil if no
// such row exists.
func (s *Store) RemoveFavorite(ctx context.Context, userID, channelID string) error {
	_, err := s.Pool.Exec(ctx, `
		DELETE FROM user_favorites WHERE user_id = $1 AND channel_id = $2
	`, userID, channelID)
	if err != nil {
		return fmt.Errorf("remove favorite: %w", err)
	}
	return nil
}

// ReorderFavorites assigns positions to the supplied channels in order
// (orderedIDs[0] -> position 0, orderedIDs[1] -> 1, ...) in a single SQL
// UPDATE driven by a VALUES list. Channels that the user does not currently
// favorite are silently ignored. The call is a no-op when orderedIDs is empty.
func (s *Store) ReorderFavorites(ctx context.Context, userID string, orderedIDs []string) error {
	if len(orderedIDs) == 0 {
		return nil
	}
	// Build "($N::text, K)" pairs for the VALUES list. The first parameter
	// slot is reserved for userID, so the channel ids start at $2.
	parts := make([]string, len(orderedIDs))
	args := make([]any, 0, len(orderedIDs)+1)
	args = append(args, userID)
	for i, id := range orderedIDs {
		args = append(args, id)
		parts[i] = fmt.Sprintf("($%d::text, %d)", i+2, i)
	}
	sql := `
		UPDATE user_favorites
		SET position = o.pos
		FROM (VALUES ` + strings.Join(parts, ", ") + `) AS o(channel_id, pos)
		WHERE user_favorites.user_id = $1
		  AND user_favorites.channel_id = o.channel_id
	`
	if _, err := s.Pool.Exec(ctx, sql, args...); err != nil {
		return fmt.Errorf("reorder favorites: %w", err)
	}
	return nil
}

// MarkTuned records that userID just started watching channelID. Idempotent:
// re-tuning updates last_tuned_at in place without creating a duplicate row.
func (s *Store) MarkTuned(ctx context.Context, userID, channelID string) error {
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO user_recent (user_id, channel_id, last_tuned_at)
		VALUES ($1, $2, now())
		ON CONFLICT (user_id, channel_id) DO UPDATE
		SET last_tuned_at = now()
	`, userID, channelID)
	if err != nil {
		return fmt.Errorf("mark tuned: %w", err)
	}
	return nil
}

// ListRecent returns the user's recently-tuned channels ordered by
// last_tuned_at DESC. limit <= 0 returns all rows.
func (s *Store) ListRecent(ctx context.Context, userID string, limit int) ([]Recent, error) {
	sql := `
		SELECT channel_id, last_tuned_at
		FROM user_recent
		WHERE user_id = $1
		ORDER BY last_tuned_at DESC, channel_id ASC
	`
	args := []any{userID}
	if limit > 0 {
		sql += " LIMIT $2"
		args = append(args, limit)
	}
	rows, err := s.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("list recent: %w", err)
	}
	defer rows.Close()
	var out []Recent
	for rows.Next() {
		var r Recent
		if err := rows.Scan(&r.ChannelID, &r.LastTunedAt); err != nil {
			return nil, fmt.Errorf("scan recent: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
