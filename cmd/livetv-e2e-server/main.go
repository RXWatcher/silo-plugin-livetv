// Command livetv-e2e-server is a Playwright-facing harness that mounts the
// plugin's chi router on a plain HTTP listener so end-to-end tests can drive
// the SPA + API + stream proxy without a Continuum host.
//
// It is NOT used in production. The production entrypoint
// (cmd/continuum-plugin-livetv) wraps the same router with the gRPC plugin
// runtime; this binary only adds:
//
//   - An auth-bypass middleware that injects X-Continuum-User-Id and
//     X-Continuum-Admin so the chi handlers' RequireSession / RequireAdmin
//     middleware accept every request.
//   - A `/` fallback that serves the embedded SPA (the gRPC capability
//     surface does that in production via the host's static file glue;
//     here we serve it ourselves so the browser can load index.html).
//   - Bind-to-`:0` and print-port-to-stdout so Playwright's webServer
//     can pick up the dynamically-assigned port.
//
// Required env:
//
//	DATABASE_URL - Postgres DSN scoped to a `livetv` schema. The harness
//	               applies migrations before starting the listener.
//
// Optional env:
//
//	E2E_LISTEN_HOST - default 127.0.0.1
//	E2E_USER_ID     - default "e2e-user"
package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"

	"github.com/hashicorp/go-hclog"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/RXWatcher/continuum-plugin-livetv/internal/migrate"
	"github.com/RXWatcher/continuum-plugin-livetv/internal/refresh"
	"github.com/RXWatcher/continuum-plugin-livetv/internal/server"
	"github.com/RXWatcher/continuum-plugin-livetv/internal/settings"
	"github.com/RXWatcher/continuum-plugin-livetv/internal/store"
	"github.com/RXWatcher/continuum-plugin-livetv/internal/streamproxy"
	web "github.com/RXWatcher/continuum-plugin-livetv/web"
)

func main() {
	logger := hclog.New(&hclog.LoggerOptions{Name: "livetv-e2e", Level: hclog.Info})

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "DATABASE_URL is required")
		os.Exit(1)
	}
	host := os.Getenv("E2E_LISTEN_HOST")
	if host == "" {
		host = "127.0.0.1"
	}
	userID := os.Getenv("E2E_USER_ID")
	if userID == "" {
		userID = "e2e-user"
	}

	ctx := context.Background()
	if err := migrate.Run(ctx, dsn); err != nil {
		fmt.Fprintf(os.Stderr, "migrate: %v\n", err)
		os.Exit(1)
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pgxpool: %v\n", err)
		os.Exit(1)
	}
	defer pool.Close()

	st := store.New(pool)
	snap, err := settings.Load(ctx, st)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load settings: %v\n", err)
		os.Exit(1)
	}

	streamDeps := &streamproxy.Deps{
		Store:    st,
		Settings: snap,
		Logger:   logger.Named("streamproxy"),
		HTTP:     http.DefaultClient,
	}
	m3uWorker := &refresh.M3UWorker{Store: st, Client: http.DefaultClient, Logger: logger.Named("m3u")}
	xmltvWorker := &refresh.XMLTVWorker{Store: st, Client: http.DefaultClient, Logger: logger.Named("xmltv")}

	srv := &server.Server{
		Store:       st,
		Stream:      streamDeps,
		Settings:    snap,
		Logger:      logger.Named("api"),
		M3UWorker:   m3uWorker,
		XMLTVWorker: xmltvWorker,
		Snapshot:    snap,
	}

	apiHandler := srv.Routes()
	spa := web.SPAHandler()

	// The plugin's chi router covers /healthz + /api/v1/livetv/*. Anything
	// else (SPA routes like /, /channels, /admin/sources) must hit the
	// embedded SPA fallback so react-router can take over.
	root := http.NewServeMux()
	root.Handle("/api/", withAuthBypass(apiHandler, userID))
	root.Handle("/healthz", apiHandler)
	root.Handle("/", spa)

	ln, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "listen: %v\n", err)
		os.Exit(1)
	}

	// Print the bound port on stdout so Playwright's webServer can read it.
	// Format: E2E_PORT=<port>\n on a line of its own; the harness JS parses
	// this line specifically.
	addr := ln.Addr().(*net.TCPAddr)
	fmt.Printf("E2E_PORT=%d\n", addr.Port)
	fmt.Printf("E2E_BASE_URL=http://%s\n", net.JoinHostPort(host, fmt.Sprint(addr.Port)))
	_ = os.Stdout.Sync()

	httpSrv := &http.Server{Handler: root}
	if err := httpSrv.Serve(ln); err != nil {
		fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		os.Exit(1)
	}
}

// withAuthBypass injects the X-Continuum-User-Id and X-Continuum-Admin
// headers on every API request so the chi RequireSession / RequireAdmin
// middleware lets the test through without a real host.
func withAuthBypass(next http.Handler, userID string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Continuum-User-Id") == "" {
			r.Header.Set("X-Continuum-User-Id", userID)
		}
		if r.Header.Get("X-Continuum-Admin") == "" {
			r.Header.Set("X-Continuum-Admin", "true")
		}
		next.ServeHTTP(w, r)
	})
}
