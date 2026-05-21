package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// SettingsRow mirrors the singleton row in the livetv settings table. The
// table has exactly one row (id=1, enforced by CHECK); these getters and
// setters operate on that row unconditionally.
//
// This is the one approved Phase 3 extension — the Phase 7 snapshot needs
// typed I/O against settings rather than scattering raw SQL across the
// scheduler and admin handler.
type SettingsRow struct {
	DefaultM3URefresh    time.Duration
	DefaultXMLTVRefresh  time.Duration
	GuideWindowCap       time.Duration
	PerUserStreamCap     int
	PerChannelDefaultCap int
	SessionIdleTimeout   time.Duration
}

// GetSettings reads the singleton settings row. The migration guarantees the
// row exists, so a missing row is reported as an error (rather than silently
// returning zero values that would mask schema drift).
func (s *Store) GetSettings(ctx context.Context) (SettingsRow, error) {
	var (
		m3uIv, xmltvIv, guideIv, idleIv pgtype.Interval
		perUser, perChannel             int
	)
	err := s.Pool.QueryRow(ctx, `
		SELECT default_m3u_refresh, default_xmltv_refresh, guide_window_cap,
		       per_user_stream_cap, per_channel_default_cap, session_idle_timeout
		FROM settings WHERE id = 1
	`).Scan(&m3uIv, &xmltvIv, &guideIv, &perUser, &perChannel, &idleIv)
	if err != nil {
		return SettingsRow{}, fmt.Errorf("get settings: %w", err)
	}
	return SettingsRow{
		DefaultM3URefresh:    intervalToDuration(m3uIv),
		DefaultXMLTVRefresh:  intervalToDuration(xmltvIv),
		GuideWindowCap:       intervalToDuration(guideIv),
		PerUserStreamCap:     perUser,
		PerChannelDefaultCap: perChannel,
		SessionIdleTimeout:   intervalToDuration(idleIv),
	}, nil
}

// UpdateSettings replaces every column on the singleton row. Validation
// (positive durations, positive ints) is the caller's responsibility; this
// method just performs the write.
func (s *Store) UpdateSettings(ctx context.Context, v SettingsRow) error {
	_, err := s.Pool.Exec(ctx, `
		UPDATE settings SET
			default_m3u_refresh     = $1,
			default_xmltv_refresh   = $2,
			guide_window_cap        = $3,
			per_user_stream_cap     = $4,
			per_channel_default_cap = $5,
			session_idle_timeout    = $6,
			updated_at              = now()
		WHERE id = 1
	`,
		durationToInterval(v.DefaultM3URefresh),
		durationToInterval(v.DefaultXMLTVRefresh),
		durationToInterval(v.GuideWindowCap),
		v.PerUserStreamCap,
		v.PerChannelDefaultCap,
		durationToInterval(v.SessionIdleTimeout),
	)
	if err != nil {
		return fmt.Errorf("update settings: %w", err)
	}
	return nil
}
