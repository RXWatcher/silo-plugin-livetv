package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ProgramRef is the minimal subset of a program needed for the now/next
// display on a channel row. The full Program type (with credits, episode
// metadata, etc.) lives in programs.go; this lightweight ref keeps the
// channels file decoupled from that schema.
type ProgramRef struct {
	ID    string
	Title string
	Start time.Time
	Stop  time.Time
}

// Channel mirrors the channels table plus user-scoped decoration that
// ListChannelsForUser populates. The Effective* fields are computed via
// coalesce(*_admin, *_src) at the SQL layer and surfaced for convenience.
type Channel struct {
	ID                  string
	SourceM3UID         string
	SourceChannelID     string
	DisplayName         string
	LogoURL             string
	UpstreamURL         string
	UpstreamKind        string
	ChannelNumberSrc    string
	ChannelNumberAdmin  *string
	GroupTitleSrc       string
	GroupTitleAdmin     *string
	Attrs               map[string]string
	EnabledSrc          bool
	EnabledAdmin        *bool
	Position            int
	EffectiveChannelNum string
	EffectiveGroupTitle string
	EffectiveEnabled    bool

	// Populated by ListChannelsForUser / GetChannelView.
	HasFavorite    bool
	CurrentProgram *ProgramRef
	NextProgram    *ProgramRef
}

// ChannelPatch describes admin overrides to apply. patchValue distinguishes
// "leave the column untouched" (Set=false) from "set this value" (Set=true).
// Setting a *string to nil with Set=true clears the override.
type ChannelPatch struct {
	ChannelNumberAdmin patchValue[*string]
	GroupTitleAdmin    patchValue[*string]
	EnabledAdmin       patchValue[*bool]
	Position           patchValue[*int]
}

type patchValue[T any] struct {
	Set   bool
	Value T
}

// SetChannelNumberAdmin returns a patchValue that sets the channel-number
// override to v (pass nil to clear).
func SetChannelNumberAdmin(v *string) patchValue[*string] {
	return patchValue[*string]{Set: true, Value: v}
}

// SetGroupTitleAdmin returns a patchValue that sets the group-title override.
func SetGroupTitleAdmin(v *string) patchValue[*string] {
	return patchValue[*string]{Set: true, Value: v}
}

// SetEnabledAdmin returns a patchValue that sets the enabled override.
func SetEnabledAdmin(v *bool) patchValue[*bool] {
	return patchValue[*bool]{Set: true, Value: v}
}

// SetPosition returns a patchValue that sets the position. Pass nil to clear
// (revert to default 0).
func SetPosition(v *int) patchValue[*int] {
	return patchValue[*int]{Set: true, Value: v}
}

// marshalAttrs renders the attrs map for jsonb binding. Mirrors the headers
// helper but in its own function so we don't tangle the two payload shapes.
func marshalAttrs(a map[string]string) ([]byte, error) {
	if a == nil {
		return []byte("{}"), nil
	}
	b, err := json.Marshal(a)
	if err != nil {
		return nil, fmt.Errorf("marshal attrs: %w", err)
	}
	return b, nil
}

func unmarshalAttrs(b []byte) (map[string]string, error) {
	out := map[string]string{}
	if len(b) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("unmarshal attrs: %w", err)
	}
	return out, nil
}

// UpsertChannelFromM3U inserts or updates a channel keyed by
// (source_m3u_id, source_channel_id). Only *_src columns and the catalog
// fields (display_name, logo_url, upstream_url, attrs) are touched —
// *_admin overrides and position are preserved across refreshes.
func (s *Store) UpsertChannelFromM3U(ctx context.Context, c Channel) (string, error) {
	if c.ID == "" {
		c.ID = newULID()
	}
	attrs, err := marshalAttrs(c.Attrs)
	if err != nil {
		return "", err
	}
	kind := c.UpstreamKind
	if kind == "" {
		kind = "unknown"
	}
	var id string
	err = s.Pool.QueryRow(ctx, `
		INSERT INTO channels (id, source_m3u_id, source_channel_id, display_name,
		    channel_number_src, logo_url, group_title_src, upstream_url,
		    upstream_kind, attrs, enabled_src)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,true)
		ON CONFLICT (source_m3u_id, source_channel_id) DO UPDATE
		SET display_name       = EXCLUDED.display_name,
		    channel_number_src = EXCLUDED.channel_number_src,
		    logo_url           = EXCLUDED.logo_url,
		    group_title_src    = EXCLUDED.group_title_src,
		    upstream_url       = EXCLUDED.upstream_url,
		    attrs              = EXCLUDED.attrs,
		    enabled_src        = true,
		    updated_at         = now()
		RETURNING id
	`, c.ID, c.SourceM3UID, c.SourceChannelID, c.DisplayName,
		c.ChannelNumberSrc, c.LogoURL, c.GroupTitleSrc, c.UpstreamURL,
		kind, attrs).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("upsert channel: %w", err)
	}
	return id, nil
}

// MarkChannelsMissing flips enabled_src=false for channels of a source whose
// source_channel_id is NOT in `seen`. Used after an M3U refresh to soft-disable
// rows the upstream playlist no longer advertises.
func (s *Store) MarkChannelsMissing(ctx context.Context, sourceID string, seen []string) error {
	if seen == nil {
		seen = []string{}
	}
	_, err := s.Pool.Exec(ctx, `
		UPDATE channels
		SET enabled_src = false, updated_at = now()
		WHERE source_m3u_id = $1
		  AND enabled_src = true
		  AND NOT (source_channel_id = ANY($2::text[]))
	`, sourceID, seen)
	if err != nil {
		return fmt.Errorf("mark channels missing: %w", err)
	}
	return nil
}

// channelCursor is the opaque cursor for ListChannelsForUser. Encodes the
// last-returned row's (position, lower(display_name), id) so the next
// page picks up immediately after.
type channelCursor struct {
	Position int    `json:"p"`
	Name     string `json:"n"`
	ID       string `json:"i"`
}

func encodeChannelCursor(c channelCursor) string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeChannelCursor(s string) (channelCursor, error) {
	var c channelCursor
	if s == "" {
		return c, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return c, fmt.Errorf("decode cursor: %w", err)
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("unmarshal cursor: %w", err)
	}
	return c, nil
}

// ListChannelsForUser returns enabled channels ordered by
// (position ASC, lower(display_name) ASC, id ASC) with optional group
// filter, free-text search, favorite flag, and current/next program.
//
// Returns the page slice and the next-cursor string ("" when no more).
func (s *Store) ListChannelsForUser(ctx context.Context, userID, group, q string, limit int, cursor string) ([]Channel, string, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	cur, err := decodeChannelCursor(cursor)
	if err != nil {
		return nil, "", err
	}

	args := []any{userID, limit + 1}
	where := []string{"coalesce(c.enabled_admin, c.enabled_src) = true"}

	if group != "" {
		args = append(args, group)
		where = append(where, fmt.Sprintf("coalesce(c.group_title_admin, c.group_title_src) = $%d", len(args)))
	}
	if q != "" {
		args = append(args, "%"+strings.ToLower(q)+"%")
		where = append(where, fmt.Sprintf("lower(c.display_name) LIKE $%d", len(args)))
	}
	if cursor != "" {
		args = append(args, cur.Position, cur.Name, cur.ID)
		// Strict greater-than tuple comparison on the deterministic key.
		where = append(where, fmt.Sprintf(
			"(c.position, lower(c.display_name), c.id) > ($%d, $%d, $%d)",
			len(args)-2, len(args)-1, len(args)))
	}

	sql := `
		SELECT c.id, c.source_m3u_id, c.source_channel_id, c.display_name,
		       c.channel_number_src, c.channel_number_admin,
		       c.logo_url, c.group_title_src, c.group_title_admin,
		       c.upstream_url, c.upstream_kind, c.attrs,
		       c.enabled_src, c.enabled_admin, c.position,
		       coalesce(c.channel_number_admin, c.channel_number_src) AS eff_num,
		       coalesce(c.group_title_admin, c.group_title_src)       AS eff_group,
		       coalesce(c.enabled_admin, c.enabled_src)                AS eff_enabled,
		       (f.channel_id IS NOT NULL) AS has_favorite
		FROM channels c
		LEFT JOIN user_favorites f ON f.user_id = $1 AND f.channel_id = c.id
		WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY c.position ASC, lower(c.display_name) ASC, c.id ASC
		LIMIT $2
	`
	rows, err := s.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, "", fmt.Errorf("list channels for user: %w", err)
	}
	defer rows.Close()

	channels := make([]Channel, 0, limit)
	for rows.Next() {
		ch, err := scanChannelRow(rows, true)
		if err != nil {
			return nil, "", err
		}
		channels = append(channels, ch)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}

	var nextCursor string
	if len(channels) > limit {
		last := channels[limit-1]
		nextCursor = encodeChannelCursor(channelCursor{
			Position: last.Position,
			Name:     strings.ToLower(last.DisplayName),
			ID:       last.ID,
		})
		channels = channels[:limit]
	}

	if err := s.decorateWithGuide(ctx, channels); err != nil {
		return nil, "", err
	}

	return channels, nextCursor, nil
}

// GetChannel returns a single channel by id without user-specific decoration.
func (s *Store) GetChannel(ctx context.Context, id string) (Channel, error) {
	row := s.Pool.QueryRow(ctx, `
		SELECT c.id, c.source_m3u_id, c.source_channel_id, c.display_name,
		       c.channel_number_src, c.channel_number_admin,
		       c.logo_url, c.group_title_src, c.group_title_admin,
		       c.upstream_url, c.upstream_kind, c.attrs,
		       c.enabled_src, c.enabled_admin, c.position,
		       coalesce(c.channel_number_admin, c.channel_number_src),
		       coalesce(c.group_title_admin, c.group_title_src),
		       coalesce(c.enabled_admin, c.enabled_src)
		FROM channels c WHERE c.id = $1
	`, id)
	ch, err := scanChannelRow(row, false)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Channel{}, ErrNotFound
		}
		return Channel{}, err
	}
	return ch, nil
}

// GetChannelView returns a channel with the favorite flag for userID and
// the current/next program populated.
func (s *Store) GetChannelView(ctx context.Context, userID, id string) (Channel, error) {
	row := s.Pool.QueryRow(ctx, `
		SELECT c.id, c.source_m3u_id, c.source_channel_id, c.display_name,
		       c.channel_number_src, c.channel_number_admin,
		       c.logo_url, c.group_title_src, c.group_title_admin,
		       c.upstream_url, c.upstream_kind, c.attrs,
		       c.enabled_src, c.enabled_admin, c.position,
		       coalesce(c.channel_number_admin, c.channel_number_src),
		       coalesce(c.group_title_admin, c.group_title_src),
		       coalesce(c.enabled_admin, c.enabled_src),
		       (f.channel_id IS NOT NULL) AS has_favorite
		FROM channels c
		LEFT JOIN user_favorites f ON f.user_id = $1 AND f.channel_id = c.id
		WHERE c.id = $2
	`, userID, id)
	ch, err := scanChannelRow(row, true)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Channel{}, ErrNotFound
		}
		return Channel{}, err
	}
	chans := []Channel{ch}
	if err := s.decorateWithGuide(ctx, chans); err != nil {
		return Channel{}, err
	}
	return chans[0], nil
}

// SetUpstreamKind updates the probed upstream kind on a channel.
func (s *Store) SetUpstreamKind(ctx context.Context, channelID, kind string) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE channels SET upstream_kind = $2, updated_at = now()
		WHERE id = $1
	`, channelID, kind)
	if err != nil {
		return fmt.Errorf("set upstream kind: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListGroups returns the distinct effective group titles for enabled channels.
func (s *Store) ListGroups(ctx context.Context) ([]string, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT DISTINCT coalesce(group_title_admin, group_title_src) AS g
		FROM channels
		WHERE coalesce(enabled_admin, enabled_src) = true
		ORDER BY g
	`)
	if err != nil {
		return nil, fmt.Errorf("list groups: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err != nil {
			return nil, fmt.Errorf("scan group: %w", err)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// AdminListChannels returns every channel (enabled or not) for the admin UI.
// Pass sourceID="" to list across every source.
func (s *Store) AdminListChannels(ctx context.Context, sourceID string) ([]Channel, error) {
	args := []any{}
	where := ""
	if sourceID != "" {
		args = append(args, sourceID)
		where = "WHERE c.source_m3u_id = $1"
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT c.id, c.source_m3u_id, c.source_channel_id, c.display_name,
		       c.channel_number_src, c.channel_number_admin,
		       c.logo_url, c.group_title_src, c.group_title_admin,
		       c.upstream_url, c.upstream_kind, c.attrs,
		       c.enabled_src, c.enabled_admin, c.position,
		       coalesce(c.channel_number_admin, c.channel_number_src),
		       coalesce(c.group_title_admin, c.group_title_src),
		       coalesce(c.enabled_admin, c.enabled_src)
		FROM channels c `+where+`
		ORDER BY c.position ASC, lower(c.display_name) ASC, c.id ASC
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("admin list channels: %w", err)
	}
	defer rows.Close()
	var out []Channel
	for rows.Next() {
		ch, err := scanChannelRow(rows, false)
		if err != nil {
			return nil, err
		}
		out = append(out, ch)
	}
	return out, rows.Err()
}

// AdminPatchChannel applies admin overrides described by patch. Patch fields
// whose Set is false are left untouched. Patch fields whose Value pointer is
// nil clear the existing override (column set to NULL).
func (s *Store) AdminPatchChannel(ctx context.Context, channelID string, patch ChannelPatch) error {
	sets := []string{}
	args := []any{channelID}
	if patch.ChannelNumberAdmin.Set {
		args = append(args, patch.ChannelNumberAdmin.Value)
		sets = append(sets, fmt.Sprintf("channel_number_admin = $%d", len(args)))
	}
	if patch.GroupTitleAdmin.Set {
		args = append(args, patch.GroupTitleAdmin.Value)
		sets = append(sets, fmt.Sprintf("group_title_admin = $%d", len(args)))
	}
	if patch.EnabledAdmin.Set {
		args = append(args, patch.EnabledAdmin.Value)
		sets = append(sets, fmt.Sprintf("enabled_admin = $%d", len(args)))
	}
	if patch.Position.Set {
		var v int
		if patch.Position.Value != nil {
			v = *patch.Position.Value
		}
		args = append(args, v)
		sets = append(sets, fmt.Sprintf("position = $%d", len(args)))
	}
	if len(sets) == 0 {
		return nil
	}
	sets = append(sets, "updated_at = now()")
	sql := "UPDATE channels SET " + strings.Join(sets, ", ") + " WHERE id = $1"
	tag, err := s.Pool.Exec(ctx, sql, args...)
	if err != nil {
		return fmt.Errorf("admin patch channel: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// AddEPGKey links a channel to an XMLTV channel id.
func (s *Store) AddEPGKey(ctx context.Context, channelID, xmltvChannelID string, autoLinked bool) error {
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO channel_epg_keys (channel_id, xmltv_channel_id, auto_linked)
		VALUES ($1, $2, $3)
		ON CONFLICT (channel_id, xmltv_channel_id) DO UPDATE
		SET auto_linked = EXCLUDED.auto_linked
	`, channelID, xmltvChannelID, autoLinked)
	if err != nil {
		return fmt.Errorf("add epg key: %w", err)
	}
	return nil
}

// RemoveEPGKey unlinks a channel from an XMLTV channel id.
func (s *Store) RemoveEPGKey(ctx context.Context, channelID, xmltvChannelID string) error {
	_, err := s.Pool.Exec(ctx, `
		DELETE FROM channel_epg_keys
		WHERE channel_id = $1 AND xmltv_channel_id = $2
	`, channelID, xmltvChannelID)
	if err != nil {
		return fmt.Errorf("remove epg key: %w", err)
	}
	return nil
}

// ListEPGKeys returns the xmltv channel ids linked to a channel.
func (s *Store) ListEPGKeys(ctx context.Context, channelID string) ([]string, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT xmltv_channel_id FROM channel_epg_keys
		WHERE channel_id = $1 ORDER BY xmltv_channel_id
	`, channelID)
	if err != nil {
		return nil, fmt.Errorf("list epg keys: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, fmt.Errorf("scan epg key: %w", err)
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// VisibleChannelIDsForUser returns the ids of enabled channels for guide
// queries. Optional group filter narrows to a single category.
func (s *Store) VisibleChannelIDsForUser(ctx context.Context, userID, group string) ([]string, error) {
	_ = userID // Phase 3: no per-user gating beyond enabled flag.
	args := []any{}
	where := []string{"coalesce(enabled_admin, enabled_src) = true"}
	if group != "" {
		args = append(args, group)
		where = append(where, fmt.Sprintf("coalesce(group_title_admin, group_title_src) = $%d", len(args)))
	}
	sql := "SELECT id FROM channels WHERE " + strings.Join(where, " AND ") +
		" ORDER BY position ASC, lower(display_name) ASC, id ASC"
	rows, err := s.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("visible channels: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan id: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// scanChannelRow scans a row produced by the SELECT projections above.
// hasFavorite controls whether the trailing has_favorite column is read.
func scanChannelRow(row pgx.Row, hasFavorite bool) (Channel, error) {
	var (
		ch    Channel
		attrs []byte
	)
	dest := []any{
		&ch.ID, &ch.SourceM3UID, &ch.SourceChannelID, &ch.DisplayName,
		&ch.ChannelNumberSrc, &ch.ChannelNumberAdmin,
		&ch.LogoURL, &ch.GroupTitleSrc, &ch.GroupTitleAdmin,
		&ch.UpstreamURL, &ch.UpstreamKind, &attrs,
		&ch.EnabledSrc, &ch.EnabledAdmin, &ch.Position,
		&ch.EffectiveChannelNum, &ch.EffectiveGroupTitle, &ch.EffectiveEnabled,
	}
	if hasFavorite {
		dest = append(dest, &ch.HasFavorite)
	}
	if err := row.Scan(dest...); err != nil {
		return Channel{}, err
	}
	a, err := unmarshalAttrs(attrs)
	if err != nil {
		return Channel{}, err
	}
	ch.Attrs = a
	return ch, nil
}

// decorateWithGuide fills the CurrentProgram / NextProgram pointers on each
// channel by querying the programs table via channel_epg_keys. The lookup
// runs as two queries (current + next) against the union of xmltv channel
// ids, then back-references results by channel id.
func (s *Store) decorateWithGuide(ctx context.Context, channels []Channel) error {
	if len(channels) == 0 {
		return nil
	}
	ids := make([]string, len(channels))
	indexByID := make(map[string]int, len(channels))
	for i, c := range channels {
		ids[i] = c.ID
		indexByID[c.ID] = i
	}
	// Current program: start_utc <= now() AND stop_utc > now()
	currentRows, err := s.Pool.Query(ctx, `
		SELECT DISTINCT ON (k.channel_id)
		       k.channel_id, p.id, p.start_utc, p.stop_utc, p.title
		FROM channel_epg_keys k
		JOIN programs p ON p.xmltv_channel_id = k.xmltv_channel_id
		WHERE k.channel_id = ANY($1::text[])
		  AND p.start_utc <= now() AND p.stop_utc > now()
		ORDER BY k.channel_id, p.start_utc DESC
	`, ids)
	if err != nil {
		return fmt.Errorf("decorate current: %w", err)
	}
	for currentRows.Next() {
		var (
			cid string
			p   ProgramRef
		)
		if err := currentRows.Scan(&cid, &p.ID, &p.Start, &p.Stop, &p.Title); err != nil {
			currentRows.Close()
			return fmt.Errorf("scan current: %w", err)
		}
		if idx, ok := indexByID[cid]; ok {
			channels[idx].CurrentProgram = &p
		}
	}
	currentRows.Close()
	if err := currentRows.Err(); err != nil {
		return err
	}

	// Next program: earliest program whose start_utc > now().
	nextRows, err := s.Pool.Query(ctx, `
		SELECT DISTINCT ON (k.channel_id)
		       k.channel_id, p.id, p.start_utc, p.stop_utc, p.title
		FROM channel_epg_keys k
		JOIN programs p ON p.xmltv_channel_id = k.xmltv_channel_id
		WHERE k.channel_id = ANY($1::text[])
		  AND p.start_utc > now()
		ORDER BY k.channel_id, p.start_utc ASC
	`, ids)
	if err != nil {
		return fmt.Errorf("decorate next: %w", err)
	}
	defer nextRows.Close()
	for nextRows.Next() {
		var (
			cid string
			p   ProgramRef
		)
		if err := nextRows.Scan(&cid, &p.ID, &p.Start, &p.Stop, &p.Title); err != nil {
			return fmt.Errorf("scan next: %w", err)
		}
		if idx, ok := indexByID[cid]; ok {
			channels[idx].NextProgram = &p
		}
	}
	return nextRows.Err()
}
