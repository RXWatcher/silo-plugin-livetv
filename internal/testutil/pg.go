// Package testutil provides shared helpers for integration tests across
// the live TV plugin's packages.
package testutil

import (
	"context"
	"testing"
	"time"

	"github.com/RXWatcher/continuum-plugin-livetv/internal/migrate"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// StartPG starts a fresh postgres:16 container, creates the `livetv`
// schema, applies the embedded migrations, and returns a pool with
// search_path=livetv. Tests are skipped when Docker is unavailable.
//
// Mirrors the pattern in internal/migrate/runner_test.go so the two
// stay consistent.
func StartPG(t *testing.T) *pgxpool.Pool {
	t.Helper()
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

	// Operator pre-creates the schema in production; mirror that here.
	bootstrap, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("bootstrap pool: %v", err)
	}
	if _, err := bootstrap.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS livetv"); err != nil {
		bootstrap.Close()
		t.Fatalf("create schema: %v", err)
	}
	bootstrap.Close()

	if err := migrate.Run(ctx, dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}
