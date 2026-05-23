package refresh

import (
	"context"
	"time"

	"github.com/hashicorp/go-hclog"

	"github.com/RXWatcher/silo-plugin-livetv/internal/store"
)

// ReapIdle ends every active stream session whose last_byte_at is older than
// idleTimeout. Returns the error from the store; the count of reaped sessions
// is logged at info level for the operator.
//
// The caller (the scheduler) is responsible for sourcing idleTimeout from the
// settings row at dispatch time so admin changes propagate without restart.
func ReapIdle(ctx context.Context, st *store.Store, idleTimeout time.Duration, logger hclog.Logger) error {
	if logger == nil {
		logger = hclog.NewNullLogger()
	}
	cutoff := time.Now().Add(-idleTimeout)
	ended, err := st.ReapIdle(ctx, cutoff)
	if err != nil {
		return err
	}
	if len(ended) > 0 {
		logger.Info("reaped idle sessions", "count", len(ended))
	}
	return nil
}
