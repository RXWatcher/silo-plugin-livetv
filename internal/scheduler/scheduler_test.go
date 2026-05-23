package scheduler_test

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pluginv1 "github.com/ContinuumApp/continuum-plugin-sdk/pkg/pluginproto/silo/plugin/v1"

	"github.com/RXWatcher/silo-plugin-livetv/internal/scheduler"
	"github.com/RXWatcher/silo-plugin-livetv/internal/store"
)

// fakeWorker records every RefreshAll invocation so tests can assert that
// dispatch routed to the expected worker (and only that one).
type fakeWorker struct {
	calls int
	err   error
}

func (f *fakeWorker) RefreshAll(_ context.Context) error {
	f.calls++
	return f.err
}

// fakeReaper plays the same role as fakeWorker but for the reap path. We can
// substitute it for the production SettingsReaper without needing a Postgres
// container in this unit test.
type fakeReaper struct {
	calls int
	err   error
}

func (f *fakeReaper) Reap(_ context.Context) error {
	f.calls++
	return f.err
}

// newServer wires a Server with the supplied test doubles. Note depsFn is a
// closure so the same Deps value is observed across every Run — exactly the
// pattern production main.go uses.
func newServer(m3u, xmltv scheduler.Worker, r scheduler.Reaper) *scheduler.Server {
	return scheduler.New(func() *scheduler.Deps {
		return &scheduler.Deps{
			// Store is non-nil so the "not configured" guard doesn't fire,
			// but the test doubles never touch it.
			Store:  &store.Store{},
			M3U:    m3u,
			XMLTV:  xmltv,
			Reaper: r,
		}
	}, nil)
}

func TestRun_DispatchesByCapabilityID(t *testing.T) {
	m3u := &fakeWorker{}
	xmltv := &fakeWorker{}
	reaper := &fakeReaper{}
	s := newServer(m3u, xmltv, reaper)

	cases := []struct {
		key    string
		assert func(t *testing.T)
	}{
		{
			key: "plugin:42:refresh_m3u_sources",
			assert: func(t *testing.T) {
				if m3u.calls != 1 {
					t.Errorf("m3u calls = %d, want 1", m3u.calls)
				}
			},
		},
		{
			key: "plugin:42:refresh_xmltv_sources",
			assert: func(t *testing.T) {
				if xmltv.calls != 1 {
					t.Errorf("xmltv calls = %d, want 1", xmltv.calls)
				}
			},
		},
		{
			key: "plugin:42:reap_idle_sessions",
			assert: func(t *testing.T) {
				if reaper.calls != 1 {
					t.Errorf("reaper calls = %d, want 1", reaper.calls)
				}
			},
		},
	}

	for _, c := range cases {
		t.Run(c.key, func(t *testing.T) {
			if _, err := s.Run(context.Background(),
				&pluginv1.RunScheduledTaskRequest{TaskKey: c.key}); err != nil {
				t.Fatalf("run %q: %v", c.key, err)
			}
			c.assert(t)
		})
	}

	// Each double should have been hit exactly once total.
	if m3u.calls != 1 || xmltv.calls != 1 || reaper.calls != 1 {
		t.Errorf("total calls m3u=%d xmltv=%d reaper=%d, want 1 each",
			m3u.calls, xmltv.calls, reaper.calls)
	}
}

func TestRun_UnknownIDIsUnimplemented(t *testing.T) {
	s := newServer(&fakeWorker{}, &fakeWorker{}, &fakeReaper{})
	_, err := s.Run(context.Background(),
		&pluginv1.RunScheduledTaskRequest{TaskKey: "plugin:42:unknown"})
	if err == nil {
		t.Fatal("expected error for unknown capability id")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status, got %T: %v", err, err)
	}
	if st.Code() != codes.Unimplemented {
		t.Errorf("code = %s, want Unimplemented", st.Code())
	}
}

func TestRun_NilDepsErrors(t *testing.T) {
	s := scheduler.New(func() *scheduler.Deps { return nil }, nil)
	if _, err := s.Run(context.Background(),
		&pluginv1.RunScheduledTaskRequest{TaskKey: "plugin:42:refresh_m3u_sources"}); err == nil {
		t.Fatal("nil deps must error so the host retries")
	}
}
