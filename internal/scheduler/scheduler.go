// Package scheduler implements the scheduled_task.v1 gRPC surface for the
// live TV plugin. It is the host's RPC bridge into the refresh package and
// the idle-session reaper: the host fires capability ids at a configured
// cadence, this server dispatches to the matching worker.
package scheduler

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pluginv1 "github.com/ContinuumApp/continuum-plugin-sdk/pkg/pluginproto/continuum/plugin/v1"

	"github.com/ContinuumApp/continuum-plugin-livetv/internal/refresh"
	"github.com/ContinuumApp/continuum-plugin-livetv/internal/store"
)

// Worker is the minimal interface the scheduler needs from a refresh worker.
// Both refresh.M3UWorker and refresh.XMLTVWorker satisfy it; the indirection
// lets tests substitute in-memory doubles without spinning up Postgres.
type Worker interface {
	RefreshAll(ctx context.Context) error
}

// Reaper is the minimal interface the scheduler needs to close idle sessions.
// Production wiring resolves the idle timeout from the settings row and calls
// refresh.ReapIdle; tests inject a double that simply records the call.
type Reaper interface {
	Reap(ctx context.Context) error
}

// Deps groups the runtime collaborators. depsFn is re-invoked on every
// dispatch so the scheduler can fail fast while the plugin is still booting
// (Store == nil before Configure).
type Deps struct {
	Store  *store.Store
	M3U    Worker
	XMLTV  Worker
	Reaper Reaper
}

// Server implements pluginv1.ScheduledTaskServer for the live TV plugin.
type Server struct {
	pluginv1.UnimplementedScheduledTaskServer
	depsFn func() *Deps
	logger hclog.Logger
}

// New constructs a scheduler server. depsFn is invoked on every Run so the
// plugin can re-derive dependencies post-Configure (Phase 7 will lean on
// this).
func New(depsFn func() *Deps, logger hclog.Logger) *Server {
	if logger == nil {
		logger = hclog.NewNullLogger()
	}
	return &Server{depsFn: depsFn, logger: logger}
}

// taskID extracts the capability id from a scheduled-task key. The host sends
// "plugin:<installationID>:<capabilityID>" (see task_registry.pluginTaskKey
// in continuum-core); bare ids may arrive from host integration tests. None
// of this plugin's task ids contain ':', so a LastIndex split is safe.
func taskID(key string) string {
	if i := strings.LastIndexByte(key, ':'); i >= 0 {
		return key[i+1:]
	}
	return key
}

// Run dispatches by capability id. Recognised ids:
//
//	refresh_m3u_sources    — re-fetch every enabled M3U source
//	refresh_xmltv_sources  — re-fetch every enabled XMLTV source
//	reap_idle_sessions     — end stream sessions idle past settings cutoff
//
// Unknown ids return codes.Unimplemented so the host can distinguish "wrong
// capability" from a transient runtime failure.
func (s *Server) Run(ctx context.Context, req *pluginv1.RunScheduledTaskRequest) (*pluginv1.RunScheduledTaskResponse, error) {
	d := s.depsFn()
	if d == nil {
		return nil, fmt.Errorf("plugin not configured yet")
	}

	id := taskID(req.GetTaskKey())
	switch id {
	case "refresh_m3u_sources":
		if d.M3U == nil {
			return nil, fmt.Errorf("m3u worker not wired")
		}
		if err := d.M3U.RefreshAll(ctx); err != nil {
			s.logger.Warn("m3u refresh", "err", err)
			return nil, err
		}
	case "refresh_xmltv_sources":
		if d.XMLTV == nil {
			return nil, fmt.Errorf("xmltv worker not wired")
		}
		if err := d.XMLTV.RefreshAll(ctx); err != nil {
			s.logger.Warn("xmltv refresh", "err", err)
			return nil, err
		}
	case "reap_idle_sessions":
		if d.Reaper == nil {
			return nil, fmt.Errorf("reaper not wired")
		}
		if err := d.Reaper.Reap(ctx); err != nil {
			s.logger.Warn("reap idle sessions", "err", err)
			return nil, err
		}
	default:
		return nil, status.Errorf(codes.Unimplemented, "unknown task key %q", req.GetTaskKey())
	}
	return &pluginv1.RunScheduledTaskResponse{}, nil
}

// SettingsReaper resolves the idle-session timeout from the settings row at
// every tick (Phase 7 will replace this with a Configure-snapshot cache) and
// delegates the actual reap to refresh.ReapIdle.
//
// Lives here rather than in the refresh package so we don't leak settings-
// table access outside the dispatch layer.
type SettingsReaper struct {
	Store  *store.Store
	Logger hclog.Logger
}

// Reap reads settings.session_idle_timeout and ends every active session
// whose last_byte_at predates the cutoff.
func (r *SettingsReaper) Reap(ctx context.Context) error {
	timeout, err := readIdleTimeout(ctx, r.Store)
	if err != nil {
		return err
	}
	return refresh.ReapIdle(ctx, r.Store, timeout, r.Logger)
}

// readIdleTimeout fetches the operator-configured idle cutoff from the
// settings row. The row is pre-seeded by migration 0001; a missing row or a
// NULL value is surfaced as an error rather than silently defaulting so the
// operator is alerted to schema drift.
//
// Lives here rather than in the store package so we don't extend the settled
// Phase 3 store contract.
func readIdleTimeout(ctx context.Context, st *store.Store) (time.Duration, error) {
	var iv pgtype.Interval
	if err := st.Pool.QueryRow(ctx,
		`SELECT session_idle_timeout FROM settings WHERE id = 1`).Scan(&iv); err != nil {
		return 0, fmt.Errorf("read settings.session_idle_timeout: %w", err)
	}
	if !iv.Valid {
		return 0, fmt.Errorf("settings.session_idle_timeout is NULL")
	}
	const day = 24 * time.Hour
	const month = 30 * day
	return time.Duration(iv.Microseconds)*time.Microsecond +
		time.Duration(iv.Days)*day +
		time.Duration(iv.Months)*month, nil
}
