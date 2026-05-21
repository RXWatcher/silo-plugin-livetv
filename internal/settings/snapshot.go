// Package settings hosts the DB-backed Snapshot that replaces
// streamproxy.StaticSettings in the production wiring. The Snapshot satisfies
// streamproxy.Settings and is refreshed by the admin PUT /admin/settings
// handler so the stream proxy and idle reaper observe runtime edits without
// requiring a restart.
package settings

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ContinuumApp/continuum-plugin-livetv/internal/store"
)

// Snapshot caches the singleton settings row in memory under a RW mutex.
// Readers (stream-proxy session minting, /guide cap, reaper tick) call the
// per-field getters; writers (admin PUT handler) call Reload after the store
// write so subsequent reads observe the new values.
type Snapshot struct {
	store *store.Store

	mu                   sync.RWMutex
	values               SettingsValues
}

// SettingsValues is the in-memory representation of the singleton settings
// row. Used both as the unit of mutation passed to Apply and as a structured
// initial value for tests that want a snapshot without a database.
type SettingsValues struct {
	DefaultM3URefresh    time.Duration
	DefaultXMLTVRefresh  time.Duration
	GuideWindowCap       time.Duration
	PerUserStreamCap     int
	PerChannelDefaultCap int
	SessionIdleTimeout   time.Duration
}

// Load constructs a Snapshot pre-populated from the singleton settings row
// in the database. The returned Snapshot retains a reference to st so
// Reload() doesn't need to be re-handed the store on every call.
func Load(ctx context.Context, st *store.Store) (*Snapshot, error) {
	s := &Snapshot{store: st}
	if err := s.Reload(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// FromValues constructs a Snapshot pre-populated with v but without an
// associated store. Reload on the returned snapshot is a no-op, which makes
// it convenient for unit tests that don't want to spin up Postgres.
func FromValues(v SettingsValues) *Snapshot {
	return &Snapshot{values: v}
}

// Reload re-reads the singleton row and overwrites the in-memory values
// under the write lock. Called by the admin PUT handler after a successful
// store.UpdateSettings.
func (s *Snapshot) Reload(ctx context.Context) error {
	if s.store == nil {
		// FromValues construction path: the snapshot is store-less so Reload
		// has nothing to do. Return nil rather than erroring so the admin
		// handler doesn't need a guard.
		return nil
	}
	row, err := s.store.GetSettings(ctx)
	if err != nil {
		return fmt.Errorf("snapshot reload: %w", err)
	}
	s.Apply(SettingsValues{
		DefaultM3URefresh:    row.DefaultM3URefresh,
		DefaultXMLTVRefresh:  row.DefaultXMLTVRefresh,
		GuideWindowCap:       row.GuideWindowCap,
		PerUserStreamCap:     row.PerUserStreamCap,
		PerChannelDefaultCap: row.PerChannelDefaultCap,
		SessionIdleTimeout:   row.SessionIdleTimeout,
	})
	return nil
}

// Apply replaces the in-memory values without touching the database. The
// caller is responsible for persisting via store.UpdateSettings; this method
// is exposed so tests can drive the snapshot directly.
func (s *Snapshot) Apply(v SettingsValues) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values = v
}

// snapshot reads the current values under the read lock and returns a copy.
// All public getters route through this so the lock scope stays trivially
// auditable.
func (s *Snapshot) snapshot() SettingsValues {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.values
}

// DefaultM3URefresh returns the per-source M3U refresh cadence used by the
// scheduler when a source row leaves refresh_interval as the default.
func (s *Snapshot) DefaultM3URefresh() time.Duration { return s.snapshot().DefaultM3URefresh }

// DefaultXMLTVRefresh mirrors DefaultM3URefresh for XMLTV sources.
func (s *Snapshot) DefaultXMLTVRefresh() time.Duration { return s.snapshot().DefaultXMLTVRefresh }

// GuideWindowCap returns the max guide window served by GET /guide.
func (s *Snapshot) GuideWindowCap() time.Duration { return s.snapshot().GuideWindowCap }

// PerUserStreamCap returns the per-user concurrent session cap.
func (s *Snapshot) PerUserStreamCap() int { return s.snapshot().PerUserStreamCap }

// PerChannelDefaultCap returns the per-channel concurrent session cap
// applied when a channel doesn't override the default.
func (s *Snapshot) PerChannelDefaultCap() int { return s.snapshot().PerChannelDefaultCap }

// SessionIdleTimeout returns the idle cutoff used by the reaper.
func (s *Snapshot) SessionIdleTimeout() time.Duration { return s.snapshot().SessionIdleTimeout }
