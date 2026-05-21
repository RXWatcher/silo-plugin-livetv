package store

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/oklog/ulid/v2"
)

// M3USource mirrors the m3u_sources table.
type M3USource struct {
	ID              string
	Name            string
	URL             string
	HTTPHeaders     map[string]string
	Enabled         bool
	RefreshInterval time.Duration
	LastRefreshedAt *time.Time
	LastStatus      string
	ETag            string
	LastModified    string
}

// XMLTVSource mirrors the xmltv_sources table.
type XMLTVSource struct {
	ID              string
	Name            string
	URL             string
	HTTPHeaders     map[string]string
	Enabled         bool
	RefreshInterval time.Duration
	LastRefreshedAt *time.Time
	LastStatus      string
	ETag            string
	LastModified    string
	Gzip            bool
}

// newULID returns a fresh monotonically-ordered ULID encoded as a string.
func newULID() string {
	return ulid.MustNew(ulid.Timestamp(time.Now()), rand.Reader).String()
}

// durationToInterval converts a Go duration into a pgtype.Interval suitable
// for binding to a Postgres interval column. We collapse the value into
// microseconds — Postgres normalises on the way in.
func durationToInterval(d time.Duration) pgtype.Interval {
	return pgtype.Interval{
		Microseconds: int64(d / time.Microsecond),
		Valid:        true,
	}
}

// intervalToDuration converts a Postgres interval back into a Go duration.
// We treat months as 30 days and days as 24 hours since the column stores
// refresh cadences, not calendar arithmetic.
func intervalToDuration(iv pgtype.Interval) time.Duration {
	if !iv.Valid {
		return 0
	}
	const day = 24 * time.Hour
	const month = 30 * day
	return time.Duration(iv.Microseconds)*time.Microsecond +
		time.Duration(iv.Days)*day +
		time.Duration(iv.Months)*month
}

// marshalHeaders renders the map as JSON bytes for jsonb binding. An empty
// map becomes "{}" (never null) so we round-trip cleanly through the column
// default.
func marshalHeaders(h map[string]string) ([]byte, error) {
	if h == nil {
		return []byte("{}"), nil
	}
	b, err := json.Marshal(h)
	if err != nil {
		return nil, fmt.Errorf("marshal headers: %w", err)
	}
	return b, nil
}

// unmarshalHeaders decodes the jsonb headers payload. An empty document
// returns a non-nil empty map so callers don't have to nil-check.
func unmarshalHeaders(b []byte) (map[string]string, error) {
	out := map[string]string{}
	if len(b) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("unmarshal headers: %w", err)
	}
	return out, nil
}

// ListM3USources returns all m3u sources ordered by name.
func (s *Store) ListM3USources(ctx context.Context) ([]M3USource, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id, name, url, http_headers, enabled, refresh_interval,
		       last_refreshed_at, last_status, etag, last_modified
		FROM m3u_sources ORDER BY name
	`)
	if err != nil {
		return nil, fmt.Errorf("list m3u_sources: %w", err)
	}
	defer rows.Close()
	var out []M3USource
	for rows.Next() {
		src, err := scanM3USource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, src)
	}
	return out, rows.Err()
}

// CreateM3USource inserts an m3u source. Assigns a ULID id if empty.
func (s *Store) CreateM3USource(ctx context.Context, src M3USource) (M3USource, error) {
	if src.ID == "" {
		src.ID = newULID()
	}
	headers, err := marshalHeaders(src.HTTPHeaders)
	if err != nil {
		return M3USource{}, err
	}
	_, err = s.Pool.Exec(ctx, `
		INSERT INTO m3u_sources (id, name, url, http_headers, enabled,
			refresh_interval, last_status, etag, last_modified)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
	`, src.ID, src.Name, src.URL, headers, src.Enabled,
		durationToInterval(src.RefreshInterval), src.LastStatus, src.ETag, src.LastModified)
	if err != nil {
		return M3USource{}, fmt.Errorf("create m3u_source: %w", err)
	}
	return s.GetM3USource(ctx, src.ID)
}

// GetM3USource reads a single m3u source. Returns ErrNotFound on miss.
func (s *Store) GetM3USource(ctx context.Context, id string) (M3USource, error) {
	row := s.Pool.QueryRow(ctx, `
		SELECT id, name, url, http_headers, enabled, refresh_interval,
		       last_refreshed_at, last_status, etag, last_modified
		FROM m3u_sources WHERE id = $1
	`, id)
	src, err := scanM3USource(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return M3USource{}, ErrNotFound
		}
		return M3USource{}, err
	}
	return src, nil
}

// UpdateM3USource overwrites the mutable fields of an m3u source.
// Status fields (last_refreshed_at, last_status, etag, last_modified) are
// updated by MarkM3UStatus; this method does not touch them.
func (s *Store) UpdateM3USource(ctx context.Context, src M3USource) error {
	headers, err := marshalHeaders(src.HTTPHeaders)
	if err != nil {
		return err
	}
	tag, err := s.Pool.Exec(ctx, `
		UPDATE m3u_sources
		SET name = $2, url = $3, http_headers = $4, enabled = $5,
		    refresh_interval = $6, updated_at = now()
		WHERE id = $1
	`, src.ID, src.Name, src.URL, headers, src.Enabled, durationToInterval(src.RefreshInterval))
	if err != nil {
		return fmt.Errorf("update m3u_source: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteM3USource removes an m3u source by id. Cascades into channels.
func (s *Store) DeleteM3USource(ctx context.Context, id string) error {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM m3u_sources WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete m3u_source: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkM3UStatus updates the per-refresh metadata fields without touching
// the user-edited config fields (name/url/enabled/refresh_interval/headers).
func (s *Store) MarkM3UStatus(ctx context.Context, id, status, etag, lastModified string, refreshed time.Time) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE m3u_sources
		SET last_refreshed_at = $2, last_status = $3, etag = $4, last_modified = $5,
		    updated_at = now()
		WHERE id = $1
	`, id, refreshed, status, etag, lastModified)
	if err != nil {
		return fmt.Errorf("mark m3u status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// scanM3USource is shared between QueryRow and Query row scans.
func scanM3USource(row pgx.Row) (M3USource, error) {
	var (
		src      M3USource
		headers  []byte
		interval pgtype.Interval
	)
	if err := row.Scan(&src.ID, &src.Name, &src.URL, &headers, &src.Enabled,
		&interval, &src.LastRefreshedAt, &src.LastStatus, &src.ETag, &src.LastModified); err != nil {
		return M3USource{}, fmt.Errorf("scan m3u_source: %w", err)
	}
	src.RefreshInterval = intervalToDuration(interval)
	h, err := unmarshalHeaders(headers)
	if err != nil {
		return M3USource{}, err
	}
	src.HTTPHeaders = h
	return src, nil
}

// ListXMLTVSources returns all xmltv sources ordered by name.
func (s *Store) ListXMLTVSources(ctx context.Context) ([]XMLTVSource, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id, name, url, http_headers, enabled, refresh_interval,
		       last_refreshed_at, last_status, etag, last_modified, gzip
		FROM xmltv_sources ORDER BY name
	`)
	if err != nil {
		return nil, fmt.Errorf("list xmltv_sources: %w", err)
	}
	defer rows.Close()
	var out []XMLTVSource
	for rows.Next() {
		src, err := scanXMLTVSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, src)
	}
	return out, rows.Err()
}

// CreateXMLTVSource inserts an xmltv source. Assigns a ULID id if empty.
func (s *Store) CreateXMLTVSource(ctx context.Context, src XMLTVSource) (XMLTVSource, error) {
	if src.ID == "" {
		src.ID = newULID()
	}
	headers, err := marshalHeaders(src.HTTPHeaders)
	if err != nil {
		return XMLTVSource{}, err
	}
	_, err = s.Pool.Exec(ctx, `
		INSERT INTO xmltv_sources (id, name, url, http_headers, enabled,
			refresh_interval, last_status, etag, last_modified, gzip)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
	`, src.ID, src.Name, src.URL, headers, src.Enabled,
		durationToInterval(src.RefreshInterval), src.LastStatus, src.ETag, src.LastModified, src.Gzip)
	if err != nil {
		return XMLTVSource{}, fmt.Errorf("create xmltv_source: %w", err)
	}
	return s.GetXMLTVSource(ctx, src.ID)
}

// GetXMLTVSource reads a single xmltv source. Returns ErrNotFound on miss.
func (s *Store) GetXMLTVSource(ctx context.Context, id string) (XMLTVSource, error) {
	row := s.Pool.QueryRow(ctx, `
		SELECT id, name, url, http_headers, enabled, refresh_interval,
		       last_refreshed_at, last_status, etag, last_modified, gzip
		FROM xmltv_sources WHERE id = $1
	`, id)
	src, err := scanXMLTVSource(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return XMLTVSource{}, ErrNotFound
		}
		return XMLTVSource{}, err
	}
	return src, nil
}

// UpdateXMLTVSource overwrites the mutable fields of an xmltv source.
func (s *Store) UpdateXMLTVSource(ctx context.Context, src XMLTVSource) error {
	headers, err := marshalHeaders(src.HTTPHeaders)
	if err != nil {
		return err
	}
	tag, err := s.Pool.Exec(ctx, `
		UPDATE xmltv_sources
		SET name = $2, url = $3, http_headers = $4, enabled = $5,
		    refresh_interval = $6, gzip = $7, updated_at = now()
		WHERE id = $1
	`, src.ID, src.Name, src.URL, headers, src.Enabled,
		durationToInterval(src.RefreshInterval), src.Gzip)
	if err != nil {
		return fmt.Errorf("update xmltv_source: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteXMLTVSource removes an xmltv source by id.
func (s *Store) DeleteXMLTVSource(ctx context.Context, id string) error {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM xmltv_sources WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete xmltv_source: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkXMLTVStatus updates per-refresh metadata fields.
func (s *Store) MarkXMLTVStatus(ctx context.Context, id, status, etag, lastModified string, refreshed time.Time) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE xmltv_sources
		SET last_refreshed_at = $2, last_status = $3, etag = $4, last_modified = $5,
		    updated_at = now()
		WHERE id = $1
	`, id, refreshed, status, etag, lastModified)
	if err != nil {
		return fmt.Errorf("mark xmltv status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func scanXMLTVSource(row pgx.Row) (XMLTVSource, error) {
	var (
		src      XMLTVSource
		headers  []byte
		interval pgtype.Interval
	)
	if err := row.Scan(&src.ID, &src.Name, &src.URL, &headers, &src.Enabled,
		&interval, &src.LastRefreshedAt, &src.LastStatus, &src.ETag,
		&src.LastModified, &src.Gzip); err != nil {
		return XMLTVSource{}, fmt.Errorf("scan xmltv_source: %w", err)
	}
	src.RefreshInterval = intervalToDuration(interval)
	h, err := unmarshalHeaders(headers)
	if err != nil {
		return XMLTVSource{}, err
	}
	src.HTTPHeaders = h
	return src, nil
}
