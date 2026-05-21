package migrate

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// TestRun_AppliesAndIsIdempotent boots a fresh postgres container, creates
// the `livetv` schema the operator owns in production, and asserts that
// Run applies cleanly the first time and is a no-op on a second invocation.
func TestRun_AppliesAndIsIdempotent(t *testing.T) {
	ctx := context.Background()

	c, err := tcpostgres.Run(ctx, "postgres:16",
		tcpostgres.WithDatabase("continuum"),
		tcpostgres.WithUsername("plugin_livetv"),
		tcpostgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Skipf("skip: docker postgres unavailable: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(c) })

	dsn, err := c.ConnectionString(ctx, "sslmode=disable&search_path=livetv")
	if err != nil {
		t.Fatalf("dsn: %v", err)
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	if _, err := pool.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS livetv"); err != nil {
		pool.Close()
		t.Fatalf("create schema: %v", err)
	}
	pool.Close()

	if err := Run(ctx, dsn); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if err := Run(ctx, dsn); err != nil {
		t.Fatalf("second Run (idempotency): %v", err)
	}
}
