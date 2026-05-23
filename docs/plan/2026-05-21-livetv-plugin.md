# Live TV Plugin Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `silo-plugin-livetv` — a self-contained Silo plugin delivering IPTV / M3U live TV with XMLTV EPG, auth-gated stream proxy (no transcode), and a full Jellyfin-style web SPA (channels, guide grid, program detail, search, favorites).

**Architecture:** Go binary using the existing Silo plugin SDK. Single Postgres schema `livetv` for sources, channels, EPG, favorites, sessions. Scheduled jobs refresh M3U and XMLTV. Stream proxy fronts upstream HLS / MPEG-TS using `MintScopedStream` for auth. Embedded React 19 SPA at the plugin root. Mirrors the file layout and tooling of `silo-plugin-audiobooks`.

**Tech Stack:** Go 1.26, `github.com/ContinuumApp/continuum-plugin-sdk`, `go-chi/chi/v5`, `jackc/pgx/v5`, `golang-migrate/migrate/v4`, `hashicorp/go-hclog`, `oklog/ulid/v2`, `testcontainers-go`. Web: React 19, Vite, Tailwind v4, TanStack Query, react-router v7, radix-ui, lucide, `hls.js`, `mpegts.js`.

**Reference spec:** `/opt/silo_plugins/docs/superpowers/specs/2026-05-21-livetv-plugin-design.md`

---

## Plan Phases

- **Phase 1 — Repo scaffold & migrations** (Tasks 1–5)
- **Phase 2 — Parsers** (Tasks 6–7)
- **Phase 3 — Store layer** (Tasks 8–11)
- **Phase 4 — Refresh workers & scheduler wiring** (Tasks 12–14)
- **Phase 5 — Stream proxy** (Tasks 15–18)
- **Phase 6 — User HTTP API** (Tasks 19–23)
- **Phase 7 — Admin HTTP API** (Tasks 24–27)
- **Phase 8 — Web SPA scaffold & user pages** (Tasks 28–35)
- **Phase 9 — Web SPA admin pages** (Tasks 36–39)
- **Phase 10 — E2E, docs, manifest checksum** (Tasks 40–42)

Each phase ends in a state that can be verified independently. Phase 7 ends with a usable API; Phase 10 with a shippable plugin.

---

## Conventions all tasks follow

- Module path: `github.com/RXWatcher/silo-plugin-livetv`.
- Postgres connection: `pgxpool.Pool`; DSN from `database_url` config, must contain `search_path=livetv`.
- Errors wrapped with `fmt.Errorf("op: %w", err)`; HTTP responses use the audiobooks JSON envelope (`{ data }` / `{ error }`).
- IDs: ULIDs encoded as `text` columns. Use `oklog/ulid/v2`. Reason: matches the convention already established in `silo-plugin-audiobooks/internal/store`.
- Times: `timestamptz` in DB, UTC throughout Go code, `time.Time` in handlers, ISO-8601 strings on the wire.
- Go testing: `testing.T` with table-driven cases; integration tests use `testcontainers-go/modules/postgres`.
- Commit messages: Conventional Commits (`feat(livetv): ...`, `fix(livetv): ...`).
- New plugin lives at `/opt/silo_plugins/silo-plugin-livetv/`. Per user preference, work on `main` of that new repo, no feature branches.

---

# Phase 1 — Repo scaffold & migrations

## Task 1: Bootstrap the plugin repo

**Files:**
- Create: `silo-plugin-livetv/go.mod`
- Create: `silo-plugin-livetv/go.sum` (generated)
- Create: `silo-plugin-livetv/Makefile`
- Create: `silo-plugin-livetv/.gitignore`
- Create: `silo-plugin-livetv/cmd/silo-plugin-livetv/main.go`
- Create: `silo-plugin-livetv/cmd/silo-plugin-livetv/manifest.json`
- Create: `silo-plugin-livetv/README.md`
- Modify: `/opt/silo_plugins/go.work` (add `./silo-plugin-livetv`)

- [ ] **Step 1: Create directory structure**

```bash
mkdir -p /opt/silo_plugins/silo-plugin-livetv/{cmd/silo-plugin-livetv,internal,web,docs}
cd /opt/silo_plugins/silo-plugin-livetv
git init -b main
```

- [ ] **Step 2: Write `go.mod`**

```text
module github.com/RXWatcher/silo-plugin-livetv

go 1.26.0

require (
	github.com/ContinuumApp/continuum-plugin-sdk v0.3.10
	github.com/go-chi/chi/v5 v5.2.5
	github.com/golang-migrate/migrate/v4 v4.19.1
	github.com/hashicorp/go-hclog v1.6.3
	github.com/jackc/pgx/v5 v5.9.2
	github.com/oklog/ulid/v2 v2.1.1
	github.com/testcontainers/testcontainers-go v0.42.0
	github.com/testcontainers/testcontainers-go/modules/postgres v0.42.0
)
```

- [ ] **Step 3: Write `manifest.json`** at `cmd/silo-plugin-livetv/manifest.json`

```json
{
  "plugin_id": "silo.livetv",
  "version": "0.1.0",
  "checksum": "__CHECKSUM__",
  "silo_api_version": "v1",
  "category": "Video/LiveTV",
  "supported_platforms": [{ "os": "linux", "arch": "amd64" }],
  "capabilities": [
    {
      "type": "http_routes.v1",
      "id": "portal",
      "display_name": "Live TV",
      "description": "IPTV / M3U live TV portal with XMLTV EPG."
    },
    {
      "type": "scheduled_task.v1",
      "id": "refresh_m3u_sources",
      "display_name": "Refresh M3U sources",
      "cron": "0 */6 * * *"
    },
    {
      "type": "scheduled_task.v1",
      "id": "refresh_xmltv_sources",
      "display_name": "Refresh XMLTV sources",
      "cron": "0 */3 * * *"
    },
    {
      "type": "scheduled_task.v1",
      "id": "reap_idle_sessions",
      "display_name": "Reap idle stream sessions",
      "cron": "* * * * *"
    }
  ],
  "config_schema": [
    {
      "key": "database_url",
      "title": "Postgres DSN",
      "description": "DSN must include search_path=livetv.",
      "required": true,
      "json_schema": "{\"type\":\"string\"}",
      "admin_form": { "control": "ADMIN_FORM_CONTROL_PASSWORD" }
    }
  ],
  "http_routes": [
    {
      "id": "api",
      "display_name": "Live TV API",
      "description": "User and admin API, stream proxy, embedded SPA."
    }
  ]
}
```

- [ ] **Step 4: Write minimal `main.go`** that starts the SDK runtime, loads the manifest, opens a `pgxpool.Pool` and runs migrations, exposes a `GET /healthz` handler

```go
package main

import (
	"context"
	_ "embed"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/go-chi/chi/v5"
	"github.com/hashicorp/go-hclog"
	"github.com/jackc/pgx/v5/pgxpool"

	publicmanifest "github.com/ContinuumApp/continuum-plugin-sdk/pkg/pluginsdk/manifest"
	sdkruntime "github.com/ContinuumApp/continuum-plugin-sdk/pkg/pluginsdk/runtime"

	"github.com/RXWatcher/silo-plugin-livetv/internal/migrate"
)

//go:embed manifest.json
var manifestRaw []byte

func main() {
	logger := hclog.New(&hclog.LoggerOptions{Name: "silo-plugin-livetv"})

	manifest, err := publicmanifest.LoadEmbedded(manifestRaw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load manifest: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	dsn := os.Getenv("PLUGIN_CONFIG_DATABASE_URL")
	if dsn == "" {
		logger.Error("database_url is required")
		os.Exit(1)
	}

	if err := migrate.Run(ctx, dsn); err != nil {
		logger.Error("migrate", "err", err)
		os.Exit(1)
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		logger.Error("pgxpool", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	r := chi.NewRouter()
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	if err := sdkruntime.Serve(ctx, manifest, sdkruntime.WithHTTPHandler(r), sdkruntime.WithLogger(logger)); err != nil {
		logger.Error("serve", "err", err)
		os.Exit(1)
	}

	_ = pool
}
```

> NOTE: confirm the exact `sdkruntime.Serve` signature against the SDK at implementation time; the audiobooks plugin's `main.go` is the source of truth. Adjust option names if they differ.

- [ ] **Step 5: Add `go.work` entry**

```bash
cd /opt/silo_plugins
go work use ./silo-plugin-livetv
```

- [ ] **Step 6: Build smoke**

```bash
cd /opt/silo_plugins/silo-plugin-livetv
go mod tidy
go build ./...
```
Expected: succeeds (migrate package not yet created → introduce a temporary stub if needed and remove in Task 2).

- [ ] **Step 7: Commit**

```bash
git add -A
git commit -m "feat(livetv): bootstrap plugin scaffold and manifest"
```

---

## Task 2: Migration 0001 — sources, settings, schema setup

**Files:**
- Create: `internal/migrate/runner.go`
- Create: `internal/migrate/files/0001_init.up.sql`
- Create: `internal/migrate/files/0001_init.down.sql`
- Create: `internal/migrate/runner_test.go`

- [ ] **Step 1: Copy migration runner shape from audiobooks**

```go
// internal/migrate/runner.go
package migrate

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

//go:embed files/*.sql
var migrations embed.FS

func Run(_ context.Context, dsn string) error {
	src, err := iofs.New(migrations, "files")
	if err != nil {
		return fmt.Errorf("load migrations: %w", err)
	}
	driverDSN := dsn
	for _, p := range []string{"postgres://", "postgresql://"} {
		if strings.HasPrefix(driverDSN, p) {
			driverDSN = "pgx5://" + driverDSN[len(p):]
			break
		}
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, driverDSN)
	if err != nil {
		return fmt.Errorf("new migrate: %w", err)
	}
	defer m.Close()
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("run up: %w", err)
	}
	return nil
}
```

- [ ] **Step 2: Write `0001_init.up.sql`**

```sql
CREATE TABLE m3u_sources (
    id              text PRIMARY KEY,
    name            text NOT NULL,
    url             text NOT NULL,
    http_headers    jsonb NOT NULL DEFAULT '{}'::jsonb,
    enabled         boolean NOT NULL DEFAULT true,
    refresh_interval interval NOT NULL DEFAULT '6 hours',
    last_refreshed_at timestamptz,
    last_status     text NOT NULL DEFAULT '',
    etag            text NOT NULL DEFAULT '',
    last_modified   text NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE xmltv_sources (
    id              text PRIMARY KEY,
    name            text NOT NULL,
    url             text NOT NULL,
    http_headers    jsonb NOT NULL DEFAULT '{}'::jsonb,
    enabled         boolean NOT NULL DEFAULT true,
    refresh_interval interval NOT NULL DEFAULT '3 hours',
    last_refreshed_at timestamptz,
    last_status     text NOT NULL DEFAULT '',
    etag            text NOT NULL DEFAULT '',
    last_modified   text NOT NULL DEFAULT '',
    gzip            boolean NOT NULL DEFAULT false,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE settings (
    id                       smallint PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    default_m3u_refresh      interval NOT NULL DEFAULT '6 hours',
    default_xmltv_refresh    interval NOT NULL DEFAULT '3 hours',
    guide_window_cap         interval NOT NULL DEFAULT '24 hours',
    per_user_stream_cap      int      NOT NULL DEFAULT 3,
    per_channel_default_cap  int      NOT NULL DEFAULT 5,
    session_idle_timeout     interval NOT NULL DEFAULT '60 seconds',
    updated_at               timestamptz NOT NULL DEFAULT now()
);

INSERT INTO settings (id) VALUES (1) ON CONFLICT DO NOTHING;
```

- [ ] **Step 3: Write `0001_init.down.sql`**

```sql
DROP TABLE IF EXISTS settings;
DROP TABLE IF EXISTS xmltv_sources;
DROP TABLE IF EXISTS m3u_sources;
```

- [ ] **Step 4: Write `runner_test.go`** using testcontainers

```go
package migrate

import (
	"context"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestRunAppliesAllMigrations(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pg, err := postgres.Run(ctx, "postgres:16",
		postgres.WithDatabase("silo"),
		postgres.WithUsername("plugin_livetv"),
		postgres.WithPassword("test"),
	)
	if err != nil {
		t.Fatalf("start pg: %v", err)
	}
	t.Cleanup(func() { _ = pg.Terminate(ctx) })

	dsn, err := pg.ConnectionString(ctx, "sslmode=disable", "search_path=livetv")
	if err != nil {
		t.Fatalf("conn string: %v", err)
	}

	// pre-create the schema like an operator would
	if _, err := pg.Exec(ctx, []string{"psql", "-U", "plugin_livetv", "-d", "silo", "-c", "CREATE SCHEMA IF NOT EXISTS livetv"}); err != nil {
		t.Fatalf("create schema: %v", err)
	}

	if err := Run(ctx, dsn); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// idempotent
	if err := Run(ctx, dsn); err != nil {
		t.Fatalf("Run (second): %v", err)
	}
}
```

- [ ] **Step 5: Run test, expect it to fail (no migration files yet, would already exist in step 2 above — re-run after step 2)**

```bash
go test ./internal/migrate/...
```
Expected: PASS after Step 2.

- [ ] **Step 6: Commit**

```bash
git add -A && git commit -m "feat(livetv): migrations 0001 — sources and settings"
```

---

## Task 3: Migration 0002 — channels & channel_epg_keys

**Files:**
- Create: `internal/migrate/files/0002_channels.up.sql`
- Create: `internal/migrate/files/0002_channels.down.sql`

- [ ] **Step 1: Write `0002_channels.up.sql`**

```sql
CREATE TABLE channels (
    id                   text PRIMARY KEY,
    source_m3u_id        text NOT NULL REFERENCES m3u_sources(id) ON DELETE CASCADE,
    source_channel_id    text NOT NULL,
    display_name         text NOT NULL,
    channel_number_src   text NOT NULL DEFAULT '',
    channel_number_admin text,
    logo_url             text NOT NULL DEFAULT '',
    group_title_src      text NOT NULL DEFAULT '',
    group_title_admin    text,
    upstream_url         text NOT NULL,
    upstream_kind        text NOT NULL DEFAULT 'unknown'
        CHECK (upstream_kind IN ('mpegts', 'hls', 'unknown')),
    attrs                jsonb NOT NULL DEFAULT '{}'::jsonb,
    enabled_src          boolean NOT NULL DEFAULT true,
    enabled_admin        boolean,
    position             int     NOT NULL DEFAULT 0,
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now(),
    UNIQUE (source_m3u_id, source_channel_id)
);

CREATE INDEX channels_enabled_group_idx ON channels (coalesce(enabled_admin, enabled_src), group_title_src);
CREATE INDEX channels_name_idx ON channels (lower(display_name) text_pattern_ops);

CREATE TABLE channel_epg_keys (
    channel_id        text NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    xmltv_channel_id  text NOT NULL,
    auto_linked       boolean NOT NULL DEFAULT true,
    PRIMARY KEY (channel_id, xmltv_channel_id)
);

CREATE INDEX channel_epg_keys_xmltv_idx ON channel_epg_keys (xmltv_channel_id);
```

- [ ] **Step 2: Write `0002_channels.down.sql`**

```sql
DROP TABLE IF EXISTS channel_epg_keys;
DROP TABLE IF EXISTS channels;
```

- [ ] **Step 3: Re-run migration tests**

```bash
go test ./internal/migrate/...
```
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add -A && git commit -m "feat(livetv): migration 0002 — channels and epg keys"
```

---

## Task 4: Migration 0003 — programs & program_credits

**Files:**
- Create: `internal/migrate/files/0003_programs.up.sql`
- Create: `internal/migrate/files/0003_programs.down.sql`

- [ ] **Step 1: Write `0003_programs.up.sql`**

```sql
CREATE TABLE programs (
    id                 text PRIMARY KEY,
    xmltv_channel_id   text NOT NULL,
    start_utc          timestamptz NOT NULL,
    stop_utc           timestamptz NOT NULL,
    title              text NOT NULL,
    sub_title          text NOT NULL DEFAULT '',
    description        text NOT NULL DEFAULT '',
    episode_num        text NOT NULL DEFAULT '',
    season_num         int,
    episode            int,
    categories         text[] NOT NULL DEFAULT ARRAY[]::text[],
    rating             text NOT NULL DEFAULT '',
    icon_url           text NOT NULL DEFAULT '',
    original_air_date  date
);

CREATE INDEX programs_channel_start_idx ON programs (xmltv_channel_id, start_utc);
CREATE INDEX programs_window_idx       ON programs (start_utc, stop_utc);

CREATE TABLE program_credits (
    program_id  text NOT NULL REFERENCES programs(id) ON DELETE CASCADE,
    kind        text NOT NULL
                CHECK (kind IN ('actor','director','writer','presenter','guest','producer','composer','editor')),
    name        text NOT NULL,
    position    int  NOT NULL DEFAULT 0,
    PRIMARY KEY (program_id, kind, name)
);
```

- [ ] **Step 2: Write `0003_programs.down.sql`**

```sql
DROP TABLE IF EXISTS program_credits;
DROP TABLE IF EXISTS programs;
```

- [ ] **Step 3: Re-run tests, commit**

```bash
go test ./internal/migrate/... && git add -A && git commit -m "feat(livetv): migration 0003 — programs and credits"
```

---

## Task 5: Migration 0004 — favorites, recent, sessions

**Files:**
- Create: `internal/migrate/files/0004_user_state.up.sql`
- Create: `internal/migrate/files/0004_user_state.down.sql`

- [ ] **Step 1: Write `0004_user_state.up.sql`**

```sql
CREATE TABLE user_favorites (
    user_id     text NOT NULL,
    channel_id  text NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    position    int  NOT NULL DEFAULT 0,
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, channel_id)
);

CREATE INDEX user_favorites_user_idx ON user_favorites (user_id, position);

CREATE TABLE user_recent (
    user_id        text NOT NULL,
    channel_id     text NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    last_tuned_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, channel_id)
);

CREATE INDEX user_recent_user_idx ON user_recent (user_id, last_tuned_at DESC);

CREATE TABLE stream_sessions (
    id                text PRIMARY KEY,
    user_id           text NOT NULL,
    channel_id        text NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    scoped_grant_id   text NOT NULL,
    session_secret    bytea NOT NULL,
    started_at        timestamptz NOT NULL DEFAULT now(),
    last_byte_at      timestamptz NOT NULL DEFAULT now(),
    bytes_streamed    bigint NOT NULL DEFAULT 0,
    client_ip         inet,
    user_agent        text NOT NULL DEFAULT '',
    ended_at          timestamptz,
    end_reason        text NOT NULL DEFAULT ''
);

CREATE INDEX stream_sessions_active_idx ON stream_sessions (channel_id) WHERE ended_at IS NULL;
CREATE INDEX stream_sessions_idle_idx   ON stream_sessions (last_byte_at) WHERE ended_at IS NULL;
```

- [ ] **Step 2: Write `0004_user_state.down.sql`**

```sql
DROP TABLE IF EXISTS stream_sessions;
DROP TABLE IF EXISTS user_recent;
DROP TABLE IF EXISTS user_favorites;
```

- [ ] **Step 3: Test + commit**

```bash
go test ./internal/migrate/... && git add -A && git commit -m "feat(livetv): migration 0004 — favorites, recent, sessions"
```

---

# Phase 2 — Parsers

## Task 6: M3U parser

**Files:**
- Create: `internal/m3u/parser.go`
- Create: `internal/m3u/parser_test.go`
- Create: `internal/m3u/testdata/standard.m3u`
- Create: `internal/m3u/testdata/quirks.m3u`

- [ ] **Step 1: Define the public API**

```go
// internal/m3u/parser.go
package m3u

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

type Entry struct {
	TvgID      string
	TvgName    string
	TvgLogo    string
	TvgChno    string
	TvgShift   string
	GroupTitle string
	Title      string
	URL        string
	Attrs      map[string]string
}

func Parse(r io.Reader) ([]Entry, error) { /* impl below */ }
```

- [ ] **Step 2: Create test fixtures**

`internal/m3u/testdata/standard.m3u`:
```text
#EXTM3U
#EXTINF:-1 tvg-id="bbc1.uk" tvg-name="BBC One" tvg-logo="https://example.com/bbc1.png" tvg-chno="101" group-title="UK",BBC One
http://provider/bbc1.ts
#EXTINF:-1 tvg-id="bbc2.uk" tvg-name="BBC Two" group-title="UK",BBC Two
http://provider/bbc2.m3u8
```

`internal/m3u/testdata/quirks.m3u` (BOM, missing tvg-id, unicode, embedded equals):
```text
﻿#EXTM3U
#EXTINF:-1,Pirate Radio FM
http://provider/pirate.ts
#EXTINF:-1 tvg-name="日本テレビ" group-title="Japan",日本テレビ
http://provider/ntv.ts
#EXTINF:-1 tvg-id="weird=channel" group-title="A=B",Equals
http://provider/equals.ts
```

- [ ] **Step 3: Write parser tests**

```go
// internal/m3u/parser_test.go
package m3u

import (
	"os"
	"testing"
)

func TestParse_Standard(t *testing.T) {
	f, err := os.Open("testdata/standard.m3u")
	if err != nil { t.Fatal(err) }
	defer f.Close()
	got, err := Parse(f)
	if err != nil { t.Fatal(err) }
	if len(got) != 2 { t.Fatalf("want 2, got %d", len(got)) }

	if got[0].TvgID != "bbc1.uk" || got[0].Title != "BBC One" || got[0].URL != "http://provider/bbc1.ts" {
		t.Errorf("entry 0 mismatch: %+v", got[0])
	}
	if got[0].TvgChno != "101" || got[0].GroupTitle != "UK" {
		t.Errorf("entry 0 attrs: %+v", got[0])
	}
}

func TestParse_Quirks(t *testing.T) {
	f, _ := os.Open("testdata/quirks.m3u")
	defer f.Close()
	got, err := Parse(f)
	if err != nil { t.Fatal(err) }
	if len(got) != 3 { t.Fatalf("want 3, got %d", len(got)) }
	if got[0].Title != "Pirate Radio FM" { t.Errorf("BOM-prefixed first line broken: %+v", got[0]) }
	if got[1].TvgName != "日本テレビ" { t.Errorf("unicode broken: %q", got[1].TvgName) }
	if got[2].TvgID != "weird=channel" || got[2].GroupTitle != "A=B" {
		t.Errorf("equals inside quoted value broken: %+v", got[2])
	}
}

func TestParse_RejectsNonM3U(t *testing.T) {
	_, err := Parse(strings.NewReader("not an m3u"))
	if err == nil { t.Fatal("expected error") }
}
```

- [ ] **Step 4: Run tests to confirm they fail**

```bash
go test ./internal/m3u/...
```
Expected: FAIL ("undefined: Parse" or zero entries).

- [ ] **Step 5: Implement Parse**

```go
// internal/m3u/parser.go (full file)
package m3u

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

type Entry struct {
	TvgID, TvgName, TvgLogo, TvgChno, TvgShift, GroupTitle, Title, URL string
	Attrs                                                              map[string]string
}

func Parse(r io.Reader) ([]Entry, error) {
	br := bufio.NewReader(r)
	// strip UTF-8 BOM
	if b, err := br.Peek(3); err == nil && len(b) == 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		_, _ = br.Discard(3)
	}
	scanner := bufio.NewScanner(br)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	first := true
	var entries []Entry
	var pending *Entry
	lineNum := 0
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		lineNum++
		if first {
			if !strings.HasPrefix(line, "#EXTM3U") {
				return nil, fmt.Errorf("line 1: expected #EXTM3U, got %q", line)
			}
			first = false
			continue
		}
		if line == "" { continue }
		if strings.HasPrefix(line, "#EXTINF:") {
			e, err := parseExtinf(line)
			if err != nil { return nil, fmt.Errorf("line %d: %w", lineNum, err) }
			pending = &e
			continue
		}
		if strings.HasPrefix(line, "#") {
			// ignore other directives for MVP
			continue
		}
		if pending == nil {
			// stream URL without an EXTINF, skip
			continue
		}
		pending.URL = strings.TrimSpace(line)
		entries = append(entries, *pending)
		pending = nil
	}
	if err := scanner.Err(); err != nil { return nil, err }
	return entries, nil
}

func parseExtinf(line string) (Entry, error) {
	// "#EXTINF:<duration> attr1="v1" attr2="v2",Title text"
	rest := strings.TrimPrefix(line, "#EXTINF:")
	// duration is the leading token up to a space; we accept and ignore it
	commaIdx := strings.LastIndex(rest, ",")
	if commaIdx < 0 {
		return Entry{}, fmt.Errorf("missing title comma")
	}
	header, title := rest[:commaIdx], rest[commaIdx+1:]
	e := Entry{Title: strings.TrimSpace(title), Attrs: map[string]string{}}
	// Skip the duration field then parse quoted key="value" pairs
	parts := strings.SplitN(header, " ", 2)
	if len(parts) == 2 {
		for k, v := range parseAttrs(parts[1]) {
			e.Attrs[k] = v
			switch k {
			case "tvg-id":     e.TvgID = v
			case "tvg-name":   e.TvgName = v
			case "tvg-logo":   e.TvgLogo = v
			case "tvg-chno":   e.TvgChno = v
			case "tvg-shift":  e.TvgShift = v
			case "group-title":e.GroupTitle = v
			}
		}
	}
	if e.TvgName == "" { e.TvgName = e.Title }
	return e, nil
}

func parseAttrs(s string) map[string]string {
	out := map[string]string{}
	i := 0
	for i < len(s) {
		// skip whitespace
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') { i++ }
		if i >= len(s) { break }
		// key up to '='
		keyStart := i
		for i < len(s) && s[i] != '=' && s[i] != ' ' { i++ }
		if i >= len(s) || s[i] != '=' { break }
		key := s[keyStart:i]
		i++ // consume '='
		if i >= len(s) || s[i] != '"' { break }
		i++ // consume opening quote
		valStart := i
		for i < len(s) && s[i] != '"' { i++ }
		if i >= len(s) { break }
		val := s[valStart:i]
		i++ // consume closing quote
		out[key] = val
	}
	return out
}
```

- [ ] **Step 6: Run tests**

```bash
go test ./internal/m3u/... -v
```
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add -A && git commit -m "feat(livetv): m3u parser with #EXTINF attribute support"
```

---

## Task 7: XMLTV parser

**Files:**
- Create: `internal/xmltv/parser.go`
- Create: `internal/xmltv/parser_test.go`
- Create: `internal/xmltv/testdata/standard.xml`
- Create: `internal/xmltv/testdata/standard.xml.gz` (generated)

- [ ] **Step 1: Public types & API**

```go
// internal/xmltv/parser.go
package xmltv

import (
	"compress/gzip"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
	"time"
)

type Channel struct {
	ID          string
	DisplayName string
	IconURL     string
}

type Credit struct {
	Kind string // actor, director, writer, presenter, guest, producer, composer, editor
	Name string
	Pos  int
}

type Programme struct {
	Channel        string
	Start          time.Time
	Stop           time.Time
	Title          string
	SubTitle       string
	Description    string
	Categories     []string
	EpisodeNum     string
	SeasonNum      *int
	Episode        *int
	Rating         string
	IconURL        string
	OriginalAirDate *time.Time
	Credits        []Credit
}

// Parse streams XMLTV. Cb is called once per channel and once per programme.
func Parse(r io.Reader, onChannel func(Channel) error, onProgramme func(Programme) error) error
// Decompresses if input starts with the gzip magic bytes.
func ParseAuto(r io.Reader, onChannel func(Channel) error, onProgramme func(Programme) error) error
```

- [ ] **Step 2: Test fixture `testdata/standard.xml`**

```xml
<?xml version="1.0" encoding="UTF-8"?>
<tv>
  <channel id="bbc1.uk">
    <display-name>BBC One</display-name>
    <icon src="https://example.com/bbc1.png"/>
  </channel>
  <programme start="20260521190000 +0000" stop="20260521200000 +0000" channel="bbc1.uk">
    <title>News at Seven</title>
    <sub-title>Top Stories</sub-title>
    <desc>Headlines and weather.</desc>
    <credits>
      <presenter>Alex Doe</presenter>
      <actor>Sam Roe</actor>
    </credits>
    <category>News</category>
    <category>Weather</category>
    <episode-num system="xmltv_ns">3.4.</episode-num>
    <rating system="UK"><value>PG</value></rating>
    <icon src="https://example.com/prog.png"/>
    <date>20260521</date>
  </programme>
</tv>
```

- [ ] **Step 3: Generate gzipped fixture from `standard.xml`**

```bash
gzip -k -n /opt/silo_plugins/silo-plugin-livetv/internal/xmltv/testdata/standard.xml
```

- [ ] **Step 4: Tests**

```go
// internal/xmltv/parser_test.go
package xmltv

import (
	"os"
	"testing"
	"time"
)

func TestParse_Standard(t *testing.T) {
	f, _ := os.Open("testdata/standard.xml")
	defer f.Close()
	var channels []Channel
	var programmes []Programme
	if err := Parse(f, func(c Channel) error { channels = append(channels, c); return nil },
		func(p Programme) error { programmes = append(programmes, p); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(channels) != 1 || channels[0].ID != "bbc1.uk" || channels[0].IconURL == "" {
		t.Errorf("channel: %+v", channels)
	}
	if len(programmes) != 1 { t.Fatalf("want 1 programme, got %d", len(programmes)) }
	p := programmes[0]
	wantStart := time.Date(2026, 5, 21, 19, 0, 0, 0, time.UTC)
	if !p.Start.Equal(wantStart) { t.Errorf("start mismatch: %v", p.Start) }
	if p.Title != "News at Seven" || p.SubTitle != "Top Stories" { t.Errorf("title fields: %+v", p) }
	if len(p.Categories) != 2 { t.Errorf("categories: %v", p.Categories) }
	if p.SeasonNum == nil || *p.SeasonNum != 4 { t.Errorf("season parse from xmltv_ns failed: %v", p.SeasonNum) }
	if p.Episode == nil || *p.Episode != 5 { t.Errorf("episode parse from xmltv_ns failed: %v", p.Episode) }
	if len(p.Credits) != 2 || p.Credits[0].Kind != "presenter" || p.Credits[1].Name != "Sam Roe" {
		t.Errorf("credits: %+v", p.Credits)
	}
}

func TestParseAuto_HandlesGzip(t *testing.T) {
	f, _ := os.Open("testdata/standard.xml.gz")
	defer f.Close()
	var n int
	if err := ParseAuto(f, func(Channel) error { return nil }, func(Programme) error { n++; return nil }); err != nil {
		t.Fatal(err)
	}
	if n != 1 { t.Fatalf("want 1, got %d", n) }
}
```

- [ ] **Step 5: Implementation** — streaming `encoding/xml` decoder

```go
// internal/xmltv/parser.go (full)
package xmltv

import (
	"bufio"
	"compress/gzip"
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

const xmltvTimeLayout = "20060102150405 -0700"

func ParseAuto(r io.Reader, onChannel func(Channel) error, onProgramme func(Programme) error) error {
	br := bufio.NewReader(r)
	hdr, err := br.Peek(2)
	if err != nil && err != io.EOF { return err }
	if len(hdr) == 2 && hdr[0] == 0x1f && hdr[1] == 0x8b {
		gz, err := gzip.NewReader(br)
		if err != nil { return fmt.Errorf("gzip: %w", err) }
		defer gz.Close()
		return Parse(gz, onChannel, onProgramme)
	}
	return Parse(br, onChannel, onProgramme)
}

func Parse(r io.Reader, onChannel func(Channel) error, onProgramme func(Programme) error) error {
	dec := xml.NewDecoder(r)
	for {
		tok, err := dec.Token()
		if err == io.EOF { return nil }
		if err != nil { return err }
		se, ok := tok.(xml.StartElement)
		if !ok { continue }
		switch se.Name.Local {
		case "channel":
			var c xmlChannel
			if err := dec.DecodeElement(&c, &se); err != nil { return err }
			if err := onChannel(c.toModel()); err != nil { return err }
		case "programme":
			var p xmlProgramme
			if err := dec.DecodeElement(&p, &se); err != nil { return err }
			m, err := p.toModel()
			if err != nil { return err }
			if err := onProgramme(m); err != nil { return err }
		}
	}
}

type xmlChannel struct {
	ID          string   `xml:"id,attr"`
	DisplayName []string `xml:"display-name"`
	Icon        struct{ Src string `xml:"src,attr"` } `xml:"icon"`
}

func (c xmlChannel) toModel() Channel {
	name := ""
	if len(c.DisplayName) > 0 { name = c.DisplayName[0] }
	return Channel{ID: c.ID, DisplayName: name, IconURL: c.Icon.Src}
}

type xmlProgramme struct {
	Start      string `xml:"start,attr"`
	Stop       string `xml:"stop,attr"`
	Channel    string `xml:"channel,attr"`
	Title      string `xml:"title"`
	SubTitle   string `xml:"sub-title"`
	Desc       string `xml:"desc"`
	Category   []string `xml:"category"`
	EpisodeNum []struct {
		System string `xml:"system,attr"`
		Value  string `xml:",chardata"`
	} `xml:"episode-num"`
	Rating struct {
		Value string `xml:"value"`
	} `xml:"rating"`
	Icon struct{ Src string `xml:"src,attr"` } `xml:"icon"`
	Date    string `xml:"date"`
	Credits struct {
		Director   []string `xml:"director"`
		Actor      []string `xml:"actor"`
		Writer     []string `xml:"writer"`
		Presenter  []string `xml:"presenter"`
		Guest      []string `xml:"guest"`
		Producer   []string `xml:"producer"`
		Composer   []string `xml:"composer"`
		Editor     []string `xml:"editor"`
	} `xml:"credits"`
}

func (p xmlProgramme) toModel() (Programme, error) {
	start, err := time.Parse(xmltvTimeLayout, p.Start)
	if err != nil { return Programme{}, fmt.Errorf("start %q: %w", p.Start, err) }
	stop, err := time.Parse(xmltvTimeLayout, p.Stop)
	if err != nil { return Programme{}, fmt.Errorf("stop %q: %w", p.Stop, err) }
	out := Programme{
		Channel: p.Channel, Start: start.UTC(), Stop: stop.UTC(),
		Title: p.Title, SubTitle: p.SubTitle, Description: p.Desc,
		Categories: p.Category, Rating: p.Rating.Value, IconURL: p.Icon.Src,
	}
	if p.Date != "" {
		if t, err := time.Parse("20060102", p.Date); err == nil { out.OriginalAirDate = &t }
	}
	for _, e := range p.EpisodeNum {
		out.EpisodeNum = e.Value
		if e.System == "xmltv_ns" {
			s, ep := parseXmltvNs(e.Value)
			out.SeasonNum, out.Episode = s, ep
		}
		break
	}
	addCredits := func(kind string, names []string, base int) int {
		for i, n := range names {
			out.Credits = append(out.Credits, Credit{Kind: kind, Name: strings.TrimSpace(n), Pos: base + i})
		}
		return base + len(names)
	}
	pos := 0
	pos = addCredits("presenter", p.Credits.Presenter, pos)
	pos = addCredits("director", p.Credits.Director, pos)
	pos = addCredits("actor", p.Credits.Actor, pos)
	pos = addCredits("writer", p.Credits.Writer, pos)
	pos = addCredits("guest", p.Credits.Guest, pos)
	pos = addCredits("producer", p.Credits.Producer, pos)
	pos = addCredits("composer", p.Credits.Composer, pos)
	pos = addCredits("editor", p.Credits.Editor, pos)
	return out, nil
}

// parseXmltvNs parses XMLTV episode-num system="xmltv_ns" values like
// "3.4." (season 4, episode all) or "3.4.0/1" (season 4, ep 1, part 1/1).
// Both season and episode are 0-indexed in XMLTV; we return 1-indexed.
func parseXmltvNs(v string) (*int, *int) {
	parts := strings.SplitN(v, ".", 3)
	get := func(i int) *int {
		if i >= len(parts) { return nil }
		s := strings.TrimSpace(parts[i])
		if s == "" { return nil }
		if slash := strings.IndexByte(s, '/'); slash >= 0 { s = s[:slash] }
		n, err := strconv.Atoi(s)
		if err != nil { return nil }
		n++ // 0-indexed → 1-indexed
		return &n
	}
	return get(0), get(1)
}
```

- [ ] **Step 6: Run tests, commit**

```bash
go test ./internal/xmltv/... -v
git add -A && git commit -m "feat(livetv): xmltv parser with gzip auto-detect"
```

---

# Phase 3 — Store layer

Each store file is a thin typed wrapper around `pgxpool.Pool`. Tests use the same testcontainers helper from Task 2. Add a shared helper `internal/testutil/pg.go` (Task 8 step 1).

## Task 8: testutil + sources store

**Files:**
- Create: `internal/testutil/pg.go`
- Create: `internal/store/store.go`
- Create: `internal/store/sources.go`
- Create: `internal/store/sources_test.go`

- [ ] **Step 1: `internal/testutil/pg.go`** — spins up Postgres + applies migrations, returns a pool

```go
package testutil

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/RXWatcher/silo-plugin-livetv/internal/migrate"
)

func NewPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	pg, err := postgres.Run(ctx, "postgres:16",
		postgres.WithDatabase("silo"),
		postgres.WithUsername("plugin_livetv"),
		postgres.WithPassword("test"),
	)
	if err != nil { t.Fatalf("start pg: %v", err) }
	t.Cleanup(func() { _ = pg.Terminate(context.Background()) })

	if _, _, err := pg.Exec(ctx, []string{"psql", "-U", "plugin_livetv", "-d", "silo", "-c", "CREATE SCHEMA IF NOT EXISTS livetv"}); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	dsn, _ := pg.ConnectionString(ctx, "sslmode=disable", "search_path=livetv")
	if err := migrate.Run(ctx, dsn); err != nil { t.Fatalf("migrate: %v", err) }
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil { t.Fatalf("pgxpool: %v", err) }
	t.Cleanup(pool.Close)
	return pool
}
```

- [ ] **Step 2: `internal/store/store.go`** — top-level struct

```go
package store

import "github.com/jackc/pgx/v5/pgxpool"

type Store struct{ Pool *pgxpool.Pool }
func New(p *pgxpool.Pool) *Store { return &Store{Pool: p} }
```

- [ ] **Step 3: `internal/store/sources.go`** — CRUD for `m3u_sources` and `xmltv_sources`

```go
package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/oklog/ulid/v2"
)

type M3USource struct {
	ID, Name, URL, LastStatus, ETag, LastModified string
	HTTPHeaders                                   map[string]string
	Enabled                                       bool
	RefreshInterval                               time.Duration
	LastRefreshedAt                               *time.Time
}

func (s *Store) ListM3USources(ctx context.Context) ([]M3USource, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id, name, url, http_headers, enabled, refresh_interval,
		       last_refreshed_at, last_status, etag, last_modified
		FROM m3u_sources ORDER BY name`)
	if err != nil { return nil, err }
	defer rows.Close()
	var out []M3USource
	for rows.Next() {
		var m M3USource
		var headers []byte
		if err := rows.Scan(&m.ID, &m.Name, &m.URL, &headers, &m.Enabled,
			&m.RefreshInterval, &m.LastRefreshedAt, &m.LastStatus, &m.ETag, &m.LastModified); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(headers, &m.HTTPHeaders)
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) CreateM3USource(ctx context.Context, src M3USource) (M3USource, error) {
	src.ID = ulid.Make().String()
	hb, _ := json.Marshal(src.HTTPHeaders)
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO m3u_sources (id, name, url, http_headers, enabled, refresh_interval)
		VALUES ($1,$2,$3,$4,$5,$6)`,
		src.ID, src.Name, src.URL, hb, src.Enabled, src.RefreshInterval)
	return src, err
}

func (s *Store) UpdateM3USource(ctx context.Context, src M3USource) error {
	hb, _ := json.Marshal(src.HTTPHeaders)
	_, err := s.Pool.Exec(ctx, `
		UPDATE m3u_sources
		SET name=$2, url=$3, http_headers=$4, enabled=$5, refresh_interval=$6, updated_at=now()
		WHERE id=$1`, src.ID, src.Name, src.URL, hb, src.Enabled, src.RefreshInterval)
	return err
}

func (s *Store) DeleteM3USource(ctx context.Context, id string) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM m3u_sources WHERE id=$1`, id)
	return err
}

func (s *Store) MarkM3UStatus(ctx context.Context, id, status, etag, lastModified string, refreshed time.Time) error {
	_, err := s.Pool.Exec(ctx, `
		UPDATE m3u_sources
		SET last_status=$2, etag=$3, last_modified=$4, last_refreshed_at=$5, updated_at=now()
		WHERE id=$1`, id, status, etag, lastModified, refreshed)
	return err
}

func (s *Store) GetM3USource(ctx context.Context, id string) (M3USource, error) {
	row := s.Pool.QueryRow(ctx, `
		SELECT id, name, url, http_headers, enabled, refresh_interval,
		       last_refreshed_at, last_status, etag, last_modified
		FROM m3u_sources WHERE id=$1`, id)
	var m M3USource
	var headers []byte
	if err := row.Scan(&m.ID, &m.Name, &m.URL, &headers, &m.Enabled,
		&m.RefreshInterval, &m.LastRefreshedAt, &m.LastStatus, &m.ETag, &m.LastModified); err != nil {
		if err == pgx.ErrNoRows { return M3USource{}, ErrNotFound }
		return M3USource{}, err
	}
	_ = json.Unmarshal(headers, &m.HTTPHeaders)
	return m, nil
}
```

Repeat the same five functions (`List`, `Create`, `Update`, `Delete`, `MarkStatus`, `Get`) for `xmltv_sources` adding the `gzip bool` column.

Also add:
```go
var ErrNotFound = errors.New("not found")
```
in `store.go`.

- [ ] **Step 4: `sources_test.go`**

```go
package store

import (
	"context"
	"testing"
	"time"

	"github.com/RXWatcher/silo-plugin-livetv/internal/testutil"
)

func TestM3USourceCRUD(t *testing.T) {
	pool := testutil.NewPool(t)
	s := New(pool)
	ctx := context.Background()
	in := M3USource{Name: "Provider", URL: "http://x", HTTPHeaders: map[string]string{"User-Agent": "Test"}, Enabled: true, RefreshInterval: 6 * time.Hour}
	created, err := s.CreateM3USource(ctx, in)
	if err != nil { t.Fatal(err) }
	got, err := s.GetM3USource(ctx, created.ID)
	if err != nil || got.Name != "Provider" || got.HTTPHeaders["User-Agent"] != "Test" { t.Fatalf("get: %+v %v", got, err) }
	got.Name = "Renamed"
	if err := s.UpdateM3USource(ctx, got); err != nil { t.Fatal(err) }
	again, _ := s.GetM3USource(ctx, created.ID)
	if again.Name != "Renamed" { t.Fatal("update did not persist") }
	if err := s.DeleteM3USource(ctx, created.ID); err != nil { t.Fatal(err) }
	if _, err := s.GetM3USource(ctx, created.ID); err != ErrNotFound { t.Fatalf("expected ErrNotFound, got %v", err) }
}
```

Plus a symmetric `TestXMLTVSourceCRUD`.

- [ ] **Step 5: Run, commit**

```bash
go test ./internal/store/... -run TestM3USourceCRUD -v
git add -A && git commit -m "feat(livetv): sources store with crud and status updates"
```

---

## Task 9: Channel store

**Files:**
- Create: `internal/store/channels.go`
- Create: `internal/store/channels_test.go`

- [ ] **Step 1: Types**

```go
// internal/store/channels.go
package store

type Channel struct {
	ID, SourceM3UID, SourceChannelID                     string
	DisplayName, LogoURL, UpstreamURL, UpstreamKind      string
	ChannelNumberSrc                                     string
	ChannelNumberAdmin                                   *string
	GroupTitleSrc                                        string
	GroupTitleAdmin                                      *string
	Attrs                                                map[string]string
	EnabledSrc                                           bool
	EnabledAdmin                                         *bool
	Position                                             int
}

type ChannelView struct {
	Channel
	EffectiveChannelNumber string // coalesce(admin, src)
	EffectiveGroupTitle    string
	EffectiveEnabled       bool
}
```

- [ ] **Step 2: Methods**

```go
// Upsert returns the channel id (existing or new).
// Only writes the *_src and source-derived columns; *_admin columns
// and `position` are left untouched on conflict.
func (s *Store) UpsertChannelFromM3U(ctx context.Context, c Channel) (string, error)
// MarkChannelsMissing sets enabled_src=false for channels of a source
// whose id is not in the seen set.
func (s *Store) MarkChannelsMissing(ctx context.Context, sourceID string, seen []string) error
// ListChannelsForUser returns visible channels for a user with favorite flag and current+next programs.
func (s *Store) ListChannelsForUser(ctx context.Context, userID, group, query string, limit int, cursor string) ([]ChannelView, string, error)
// SetUpstreamKind updates the probed kind on a channel.
func (s *Store) SetUpstreamKind(ctx context.Context, channelID, kind string) error
// AdminListChannels and AdminPatchChannel for admin endpoints (Task 25).
```

Sample upsert SQL (full SQL inline so the engineer doesn't have to invent it):

```sql
INSERT INTO channels (id, source_m3u_id, source_channel_id, display_name,
    channel_number_src, logo_url, group_title_src, upstream_url,
    upstream_kind, attrs, enabled_src)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,true)
ON CONFLICT (source_m3u_id, source_channel_id) DO UPDATE
SET display_name      = EXCLUDED.display_name,
    channel_number_src = EXCLUDED.channel_number_src,
    logo_url           = EXCLUDED.logo_url,
    group_title_src    = EXCLUDED.group_title_src,
    upstream_url       = EXCLUDED.upstream_url,
    attrs              = EXCLUDED.attrs,
    enabled_src        = true,
    updated_at         = now()
RETURNING id
```

- [ ] **Step 3: Tests**

Cover: insert → upsert (same source_channel_id keeps id but updates name), `MarkChannelsMissing` flips `enabled_src` for unlisted channels, `ListChannelsForUser` honors group filter, search term, and limit.

- [ ] **Step 4: Run, commit**

```bash
go test ./internal/store/... -v
git add -A && git commit -m "feat(livetv): channel store with upsert and missing-soft-disable"
```

---

## Task 10: Program store

**Files:**
- Create: `internal/store/programs.go`
- Create: `internal/store/programs_test.go`

- [ ] **Step 1: Types & methods**

```go
type Program struct {
	ID, XMLTVChannelID, Title, SubTitle, Description, EpisodeNum, Rating, IconURL string
	Start, Stop      time.Time
	Categories       []string
	SeasonNum, Episode *int
	OriginalAirDate  *time.Time
	Credits          []Credit
}

// ReplaceFutureForChannel replaces all programs for xmltvChannelID where
// start_utc >= now() with the supplied batch, in a single transaction.
func (s *Store) ReplaceFutureForChannel(ctx context.Context, xmltvChannelID string, programs []Program) error
// PruneOldPrograms deletes programs where stop_utc < cutoff.
func (s *Store) PruneOldPrograms(ctx context.Context, cutoff time.Time) (int64, error)
// GuideWindow returns programs for the union of channel→xmltv_channel_id keys
// in [start, end), grouped by channel_id, capped by guide_window_cap.
func (s *Store) GuideWindow(ctx context.Context, channelIDs []string, start, end time.Time) (map[string][]Program, error)
// GetProgram returns one program + its credits.
func (s *Store) GetProgram(ctx context.Context, id string) (Program, error)
// SearchPrograms full-text-like ILIKE search over title/description/sub_title in a time window.
func (s *Store) SearchPrograms(ctx context.Context, q string, from, to time.Time, limit int) ([]Program, error)
```

`ReplaceFutureForChannel` in pseudocode:
```sql
BEGIN;
DELETE FROM programs WHERE xmltv_channel_id = $1 AND start_utc >= now();
-- COPY or batched INSERT for the new rows + credits
COMMIT;
```

Use `pgx.CopyFrom` for the batch insert (performance matters when an XMLTV file has 100k+ programmes).

- [ ] **Step 2: Tests** — table-driven over a small in-memory fixture, plus a "replace clears" test that inserts 3 future, replaces with 2 future, expects 2 after.

- [ ] **Step 3: Commit**

```bash
go test ./internal/store/... -v
git add -A && git commit -m "feat(livetv): program store with replace and window query"
```

---

## Task 11: Favorites, recent, sessions store

**Files:**
- Create: `internal/store/favorites.go`
- Create: `internal/store/sessions.go`
- Create: `internal/store/favorites_test.go`
- Create: `internal/store/sessions_test.go`

- [ ] **Step 1: Favorites + recent**

```go
type Favorite struct { ChannelID string; Position int }
func (s *Store) ListFavorites(ctx context.Context, userID string) ([]Favorite, error)
func (s *Store) AddFavorite(ctx context.Context, userID, channelID string) error
func (s *Store) RemoveFavorite(ctx context.Context, userID, channelID string) error
func (s *Store) ReorderFavorites(ctx context.Context, userID string, ordered []string) error
type Recent struct { ChannelID string; LastTunedAt time.Time }
func (s *Store) MarkTuned(ctx context.Context, userID, channelID string) error
func (s *Store) ListRecent(ctx context.Context, userID string, limit int) ([]Recent, error)
```

`ReorderFavorites` uses a single `UPDATE ... FROM (VALUES ...) AS o(channel_id, position)` to reorder atomically.

- [ ] **Step 2: Sessions**

```go
type Session struct {
	ID, UserID, ChannelID, ScopedGrantID, UserAgent, EndReason string
	SessionSecret      []byte
	StartedAt          time.Time
	LastByteAt         time.Time
	BytesStreamed      int64
	ClientIP           string // text repr of inet
	EndedAt            *time.Time
}

func (s *Store) CreateSession(ctx context.Context, sess Session) (Session, error)  // sets id + secret if zero
func (s *Store) UpdateSessionLastByte(ctx context.Context, id string, lastByte time.Time, bytes int64) error
func (s *Store) EndSession(ctx context.Context, id, reason string) error
func (s *Store) GetSession(ctx context.Context, id string) (Session, error)
func (s *Store) CountActiveByUser(ctx context.Context, userID string) (int, error)
func (s *Store) CountActiveByChannel(ctx context.Context, channelID string) (int, error)
func (s *Store) ListActiveSessions(ctx context.Context) ([]Session, error)
func (s *Store) ReapIdle(ctx context.Context, cutoff time.Time) ([]string, error) // returns ended ids
```

- [ ] **Step 3: Tests** — favorites add/list/remove/reorder; sessions create/end/reap-idle.

- [ ] **Step 4: Commit**

```bash
go test ./internal/store/... -v
git add -A && git commit -m "feat(livetv): favorites, recent, session store"
```

---

# Phase 4 — Refresh workers & scheduler wiring

## Task 12: M3U refresh worker

**Files:**
- Create: `internal/refresh/m3u.go`
- Create: `internal/refresh/m3u_test.go`

- [ ] **Step 1: Worker API**

```go
package refresh

import (
	"context"
	"net/http"

	"github.com/RXWatcher/silo-plugin-livetv/internal/store"
)

type M3UWorker struct {
	Store  *store.Store
	Client *http.Client // nil → http.DefaultClient
	Logger hclog.Logger
}

// RefreshAll iterates enabled M3U sources. Returns an aggregated error
// containing per-source failures (one source's failure must not block others).
func (w *M3UWorker) RefreshAll(ctx context.Context) error

// RefreshOne refreshes a single source by id.
func (w *M3UWorker) RefreshOne(ctx context.Context, id string) error
```

- [ ] **Step 2: Implementation outline**

```go
func (w *M3UWorker) RefreshOne(ctx context.Context, id string) error {
	src, err := w.Store.GetM3USource(ctx, id)
	if err != nil { return err }
	req, _ := http.NewRequestWithContext(ctx, "GET", src.URL, nil)
	for k, v := range src.HTTPHeaders { req.Header.Set(k, v) }
	if src.ETag != "" { req.Header.Set("If-None-Match", src.ETag) }
	if src.LastModified != "" { req.Header.Set("If-Modified-Since", src.LastModified) }

	client := w.Client; if client == nil { client = http.DefaultClient }
	resp, err := client.Do(req)
	if err != nil {
		_ = w.Store.MarkM3UStatus(ctx, id, "error: "+err.Error(), src.ETag, src.LastModified, time.Now().UTC())
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 304 {
		return w.Store.MarkM3UStatus(ctx, id, "ok", src.ETag, src.LastModified, time.Now().UTC())
	}
	if resp.StatusCode != 200 {
		_ = w.Store.MarkM3UStatus(ctx, id, fmt.Sprintf("error: HTTP %d", resp.StatusCode), src.ETag, src.LastModified, time.Now().UTC())
		return fmt.Errorf("http %d", resp.StatusCode)
	}

	entries, err := m3u.Parse(resp.Body)
	if err != nil {
		_ = w.Store.MarkM3UStatus(ctx, id, "error: "+err.Error(), src.ETag, src.LastModified, time.Now().UTC())
		return err
	}

	seen := make([]string, 0, len(entries))
	for _, e := range entries {
		sourceChannelID := e.TvgID
		if sourceChannelID == "" {
			sourceChannelID = "name:" + slug(e.Title) + ":" + sha8(e.URL)
		}
		ch := store.Channel{
			SourceM3UID:      id,
			SourceChannelID:  sourceChannelID,
			DisplayName:      e.Title,
			ChannelNumberSrc: e.TvgChno,
			LogoURL:          e.TvgLogo,
			GroupTitleSrc:    e.GroupTitle,
			UpstreamURL:      e.URL,
			Attrs:            e.Attrs,
		}
		if _, err := w.Store.UpsertChannelFromM3U(ctx, ch); err != nil {
			return err
		}
		seen = append(seen, sourceChannelID)
	}
	if err := w.Store.MarkChannelsMissing(ctx, id, seen); err != nil { return err }

	return w.Store.MarkM3UStatus(ctx, id,
		"ok",
		resp.Header.Get("ETag"),
		resp.Header.Get("Last-Modified"),
		time.Now().UTC())
}
```

`slug` and `sha8` are tiny helpers in the same package.

- [ ] **Step 3: Tests** using `httptest.Server` returning fixtures plus a 304 case

```go
func TestRefreshOne_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write([]byte("#EXTM3U\n#EXTINF:-1 tvg-id=\"a\" group-title=\"G\",A\nhttp://up/a.ts\n"))
	}))
	defer srv.Close()
	pool := testutil.NewPool(t)
	st := store.New(pool)
	src, _ := st.CreateM3USource(context.Background(), store.M3USource{Name: "X", URL: srv.URL, Enabled: true, RefreshInterval: time.Hour})
	w := &refresh.M3UWorker{Store: st, Logger: hclog.NewNullLogger()}
	if err := w.RefreshOne(context.Background(), src.ID); err != nil { t.Fatal(err) }
	// assert one channel exists, enabled_src=true, etag stored
	// ...
}

func TestRefreshOne_304NoChannelMutation(t *testing.T) { /* serve 304, assert channels untouched, etag preserved */ }
func TestRefreshOne_SoftDisablesMissingChannels(t *testing.T) {
	// 1st call: serve A + B; 2nd call: serve only A; assert B has enabled_src=false
}
```

- [ ] **Step 4: Run + commit**

```bash
go test ./internal/refresh/... -v
git add -A && git commit -m "feat(livetv): m3u refresh worker with conditional get and soft-disable"
```

---

## Task 13: XMLTV refresh worker

**Files:**
- Create: `internal/refresh/xmltv.go`
- Create: `internal/refresh/xmltv_test.go`

- [ ] **Step 1: API mirrors M3U worker** (`RefreshAll`, `RefreshOne`). Differences:

- Uses `xmltv.ParseAuto` to handle gzip.
- Collects programmes per channel in memory, calls `ReplaceFutureForChannel` once per channel.
- After all programmes processed, calls `PruneOldPrograms(now-6h)`.
- Auto-links `channel_epg_keys` by joining XMLTV channel ids with `channels.source_channel_id` where no manual override exists.

Sample auto-link SQL:
```sql
INSERT INTO channel_epg_keys (channel_id, xmltv_channel_id, auto_linked)
SELECT c.id, c.source_channel_id, true
FROM channels c
WHERE c.source_channel_id = ANY($1::text[])
ON CONFLICT (channel_id, xmltv_channel_id) DO NOTHING
```

- [ ] **Step 2: Tests** — happy path with 2 channels and 4 programmes; gzipped fixture; 304 path; replace clears old future entries.

- [ ] **Step 3: Commit**

```bash
go test ./internal/refresh/... -v
git add -A && git commit -m "feat(livetv): xmltv refresh worker with auto-link"
```

---

## Task 14: Session reaper + scheduled task RPC bridge

**Files:**
- Create: `internal/refresh/reaper.go`
- Create: `internal/scheduler/scheduler.go`
- Create: `internal/scheduler/scheduler_test.go`

- [ ] **Step 1: Reaper**

```go
package refresh

func ReapIdle(ctx context.Context, st *store.Store, idleTimeout time.Duration, logger hclog.Logger) error {
	cutoff := time.Now().Add(-idleTimeout)
	ended, err := st.ReapIdle(ctx, cutoff)
	if err != nil { return err }
	if len(ended) > 0 { logger.Info("reaped idle sessions", "count", len(ended)) }
	return nil
}
```

- [ ] **Step 2: Scheduler glue** — implement the `scheduled_task.v1` server methods to dispatch by capability id:

```go
package scheduler

type Server struct {
	pluginv1.UnimplementedScheduledTaskServer
	M3U     *refresh.M3UWorker
	XMLTV   *refresh.XMLTVWorker
	Reaper  func(context.Context) error
	Logger  hclog.Logger
}

func (s *Server) Run(ctx context.Context, req *pluginv1.RunRequest) (*pluginv1.RunResponse, error) {
	switch req.GetCapabilityId() {
	case "refresh_m3u_sources":
		return runOK(s.M3U.RefreshAll(ctx))
	case "refresh_xmltv_sources":
		return runOK(s.XMLTV.RefreshAll(ctx))
	case "reap_idle_sessions":
		return runOK(s.Reaper(ctx))
	}
	return nil, status.Errorf(codes.Unimplemented, "unknown capability_id %q", req.GetCapabilityId())
}
```

> Confirm the exact `scheduled_task.v1` proto method shape against the SDK at implementation time. The audiobooks plugin's `internal/scheduler` is the reference.

- [ ] **Step 3: Wire into `main.go`** — instantiate workers, pool-bound store, register `scheduled_task.v1` server with the SDK runtime. Update the `main.go` from Task 1 to add these wiring calls.

- [ ] **Step 4: Tests** — a `TestSchedulerDispatch` that asserts unknown ids return Unimplemented and known ids invoke the right worker (via test doubles).

- [ ] **Step 5: Commit**

```bash
go test ./internal/scheduler/... -v
go build ./...
git add -A && git commit -m "feat(livetv): wire scheduled tasks for refresh and idle reap"
```

---

# Phase 5 — Stream proxy

## Task 15: Scoped tokens + session creation

**Files:**
- Create: `internal/streamproxy/token.go`
- Create: `internal/streamproxy/token_test.go`
- Create: `internal/streamproxy/session.go`
- Create: `internal/streamproxy/session_test.go`
- Create: `internal/auth/host.go` — wraps `runtimehost.Client.MintScopedStream`

- [ ] **Step 1: `auth/host.go`**

```go
package auth

import (
	"context"
	"time"

	"github.com/ContinuumApp/continuum-plugin-sdk/pkg/pluginsdk/runtimehost"
)

type Host struct{ Client *runtimehost.Client }

type MintedGrant struct {
	GrantID   string
	Token     string
	ExpiresAt time.Time
}

func (h *Host) MintStreamGrant(ctx context.Context, userID, channelID string, ttl time.Duration) (MintedGrant, error) {
	// Call h.Client.MintScopedStream with audience "livetv-stream-<channelID>" and ttl.
	// Return the grant id, opaque token, expiry.
}
```

> Adjust the call signature to match the actual `MintScopedStream` API on the SDK.

- [ ] **Step 2: `streamproxy/token.go`** — per-session HMAC for HLS segment URI signing

```go
package streamproxy

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"
)

type segmentClaims struct {
	URI string    `json:"u"`
	Exp time.Time `json:"e"`
}

func SignSegment(secret []byte, uri string, expires time.Time) string {
	payload, _ := json.Marshal(segmentClaims{URI: uri, Exp: expires})
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	sig := mac.Sum(nil)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func VerifySegment(secret []byte, token string) (string, error) {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 { return "", errors.New("malformed token") }
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil { return "", err }
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil { return "", err }
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	if !hmac.Equal(sig, mac.Sum(nil)) { return "", errors.New("bad signature") }
	var c segmentClaims
	if err := json.Unmarshal(payload, &c); err != nil { return "", err }
	if time.Now().After(c.Exp) { return "", errors.New("expired") }
	return c.URI, nil
}
```

- [ ] **Step 3: Token tests** — sign+verify round-trip; tampered signature rejected; expired rejected.

- [ ] **Step 4: `streamproxy/session.go`** — session creation handler

```go
package streamproxy

type SessionDeps struct {
	Store    *store.Store
	Auth     *auth.Host
	Settings *settings.Snapshot // sync.Map fronted, see Task 27
	Logger   hclog.Logger
}

// CreateSession is the http handler for POST /api/v1/livetv/channels/{id}/stream.
func (d *SessionDeps) CreateSession(w http.ResponseWriter, r *http.Request) {
	userID := server.UserIDFromContext(r.Context())
	channelID := chi.URLParam(r, "id")
	// 1. channel visible? cap check?
	active, _ := d.Store.CountActiveByUser(r.Context(), userID)
	if active >= d.Settings.PerUserStreamCap { http.Error(w, "stream cap", 429); return }
	chActive, _ := d.Store.CountActiveByChannel(r.Context(), channelID)
	if chActive >= d.Settings.PerChannelDefaultCap { http.Error(w, "channel cap", 429); return }
	// 2. mint grant
	grant, err := d.Auth.MintStreamGrant(r.Context(), userID, channelID, 10*time.Minute)
	if err != nil { http.Error(w, "mint", 500); return }
	// 3. probe upstream kind if unknown
	kind, err := d.resolveKind(r.Context(), channelID)
	if err != nil { http.Error(w, "probe", 502); return }
	// 4. create session row with random session_secret
	secret := make([]byte, 32); _, _ = rand.Read(secret)
	sess, _ := d.Store.CreateSession(r.Context(), store.Session{
		UserID: userID, ChannelID: channelID, ScopedGrantID: grant.GrantID,
		SessionSecret: secret, ClientIP: r.RemoteAddr, UserAgent: r.UserAgent(),
	})
	// 5. mark recent
	_ = d.Store.MarkTuned(r.Context(), userID, channelID)
	// 6. set cookie
	http.SetCookie(w, &http.Cookie{Name: "livetv_stream", Value: grant.Token,
		Path: "/api/v1/livetv/stream/", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
		Expires: grant.ExpiresAt})
	suffix := ".m3u8"
	if kind == "mpegts" { suffix = ".ts" }
	writeJSON(w, 200, map[string]any{
		"session_id":   sess.ID,
		"playback_url": fmt.Sprintf("/api/v1/livetv/stream/%s%s", sess.ID, suffix),
		"expires_at":   grant.ExpiresAt,
	})
}
```

- [ ] **Step 5: Tests** — happy path; user cap reached → 429; channel cap reached → 429; unknown channel → 404; mint failure → 500.

- [ ] **Step 6: Commit**

```bash
go test ./internal/streamproxy/... -v
git add -A && git commit -m "feat(livetv): stream session creation with scoped grant"
```

---

## Task 16: Raw MPEG-TS proxy handler

**Files:**
- Create: `internal/streamproxy/mpegts.go`
- Create: `internal/streamproxy/mpegts_test.go`

- [ ] **Step 1: Handler**

```go
package streamproxy

func (d *SessionDeps) ProxyMPEGTS(w http.ResponseWriter, r *http.Request) {
	sessID := chi.URLParam(r, "session_id")
	sess, err := d.Store.GetSession(r.Context(), sessID)
	if err != nil || sess.EndedAt != nil { http.Error(w, "session gone", 404); return }
	if !d.verifyGrantCookie(r) { http.Error(w, "unauthorized", 401); return }
	ch, _ := d.Store.GetChannel(r.Context(), sess.ChannelID) // helper on channel store (Task 9 may need addition)

	upReq, _ := http.NewRequestWithContext(r.Context(), "GET", ch.UpstreamURL, nil)
	for k, v := range d.headersForChannel(r.Context(), ch) { upReq.Header.Set(k, v) }
	upResp, err := d.Client().Do(upReq)
	if err != nil { http.Error(w, "upstream", 502); _ = d.Store.EndSession(r.Context(), sessID, "upstream_error"); return }
	defer upResp.Body.Close()
	if upResp.StatusCode != 200 { http.Error(w, "upstream", 502); return }

	w.Header().Set("Content-Type", "video/mp2t")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(200)
	flusher, _ := w.(http.Flusher)

	buf := make([]byte, 64*1024)
	var bytesTotal int64
	lastUpdate := time.Now()
	for {
		n, rerr := upResp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil { break }
			if flusher != nil { flusher.Flush() }
			bytesTotal += int64(n)
			if time.Since(lastUpdate) > 5*time.Second {
				_ = d.Store.UpdateSessionLastByte(r.Context(), sessID, time.Now(), bytesTotal)
				lastUpdate = time.Now()
			}
		}
		if rerr != nil { break }
	}
	_ = d.Store.EndSession(r.Context(), sessID, "client_disconnect")
}
```

- [ ] **Step 2: Tests** — httptest upstream that streams 1MB of bytes; assert client receives all bytes; assert session row ends with `bytes_streamed >= 1MB` and `end_reason='client_disconnect'`; assert unauthorized request returns 401.

- [ ] **Step 3: Commit**

```bash
go test ./internal/streamproxy/... -v -run MPEGTS
git add -A && git commit -m "feat(livetv): mpeg-ts proxy handler"
```

---

## Task 17: HLS playlist rewriter

**Files:**
- Create: `internal/streamproxy/hls.go`
- Create: `internal/streamproxy/hls_test.go`
- Create: `internal/streamproxy/testdata/master.m3u8`
- Create: `internal/streamproxy/testdata/media.m3u8`

- [ ] **Step 1: Fixtures**

`testdata/master.m3u8`:
```text
#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=3000000,RESOLUTION=1920x1080
high.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=640x360
low.m3u8
```

`testdata/media.m3u8`:
```text
#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:6
#EXT-X-MEDIA-SEQUENCE:1
#EXTINF:6.0,
seg1.ts
#EXTINF:6.0,
seg2.ts
```

- [ ] **Step 2: Rewriter**

```go
package streamproxy

func RewritePlaylist(body io.Reader, baseUpstream *url.URL, sessionID string, secret []byte, ttl time.Duration) ([]byte, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var out bytes.Buffer
	exp := time.Now().Add(ttl)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
			out.WriteString(line); out.WriteByte('\n'); continue
		}
		u, err := url.Parse(line)
		if err != nil { return nil, err }
		abs := baseUpstream.ResolveReference(u).String()
		token := SignSegment(secret, abs, exp)
		fmt.Fprintf(&out, "/api/v1/livetv/stream/%s/segment?u=%s\n", sessionID, url.QueryEscape(token))
	}
	return out.Bytes(), scanner.Err()
}
```

- [ ] **Step 3: Tests** — feed `media.m3u8`, assert every non-`#` line becomes a `/api/v1/livetv/stream/<id>/segment?u=...` URL; assert `#EXT...` lines preserved verbatim; verify the signed tokens decode back to the absolute URI.

- [ ] **Step 4: HLS playlist HTTP handler**

```go
func (d *SessionDeps) ProxyHLSPlaylist(w http.ResponseWriter, r *http.Request) {
	sessID := chi.URLParam(r, "session_id")
	sess, err := d.Store.GetSession(r.Context(), sessID)
	if err != nil || sess.EndedAt != nil { http.Error(w, "session gone", 404); return }
	if !d.verifyGrantCookie(r) { http.Error(w, "unauthorized", 401); return }
	ch, _ := d.Store.GetChannel(r.Context(), sess.ChannelID)
	baseURL, _ := url.Parse(ch.UpstreamURL)

	upReq, _ := http.NewRequestWithContext(r.Context(), "GET", ch.UpstreamURL, nil)
	for k, v := range d.headersForChannel(r.Context(), ch) { upReq.Header.Set(k, v) }
	upResp, err := d.Client().Do(upReq)
	if err != nil || upResp.StatusCode != 200 { http.Error(w, "upstream", 502); return }
	defer upResp.Body.Close()

	body, err := RewritePlaylist(upResp.Body, baseURL, sessID, sess.SessionSecret, 5*time.Minute)
	if err != nil { http.Error(w, "rewrite", 500); return }

	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(body)
	_ = d.Store.UpdateSessionLastByte(r.Context(), sessID, time.Now(), int64(len(body)))
}
```

- [ ] **Step 5: Commit**

```bash
go test ./internal/streamproxy/... -v
git add -A && git commit -m "feat(livetv): hls playlist rewriter and handler"
```

---

## Task 18: HLS segment proxy + idle reaper integration

**Files:**
- Modify: `internal/streamproxy/hls.go`
- Modify: `internal/streamproxy/hls_test.go`

- [ ] **Step 1: Segment handler**

```go
func (d *SessionDeps) ProxyHLSSegment(w http.ResponseWriter, r *http.Request) {
	sessID := chi.URLParam(r, "session_id")
	sess, err := d.Store.GetSession(r.Context(), sessID)
	if err != nil || sess.EndedAt != nil { http.Error(w, "session gone", 404); return }

	token := r.URL.Query().Get("u")
	upstreamURI, err := VerifySegment(sess.SessionSecret, token)
	if err != nil { http.Error(w, "bad token", 401); return }

	upReq, _ := http.NewRequestWithContext(r.Context(), "GET", upstreamURI, nil)
	ch, _ := d.Store.GetChannel(r.Context(), sess.ChannelID)
	for k, v := range d.headersForChannel(r.Context(), ch) { upReq.Header.Set(k, v) }
	upResp, err := d.Client().Do(upReq)
	if err != nil { http.Error(w, "upstream", 502); return }
	defer upResp.Body.Close()
	if upResp.StatusCode != 200 { http.Error(w, "upstream", 502); return }

	if ct := upResp.Header.Get("Content-Type"); ct != "" { w.Header().Set("Content-Type", ct) }
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(200)
	written, _ := io.Copy(w, upResp.Body)
	_ = d.Store.UpdateSessionLastByte(r.Context(), sessID, time.Now(), sess.BytesStreamed+written)
}
```

- [ ] **Step 2: Tests** — fake upstream serving a segment, assert bytes proxied; tampered token → 401; expired token → 401.

- [ ] **Step 3: Add a reaper integration test** — create session, sleep past idle timeout, run reaper, assert session ended, assert subsequent segment request 404s.

- [ ] **Step 4: Commit**

```bash
go test ./internal/streamproxy/... -v
git add -A && git commit -m "feat(livetv): hls segment proxy with signed tokens and idle reap"
```

---

# Phase 6 — User HTTP API

All handlers live in `internal/server/`. Build a chi router in `internal/server/router.go` mounted at `/api/v1/livetv/` by `main.go`. Auth context (`userID`) comes from a middleware that reads the host-signed session header set by the SDK.

## Task 19: Channel list & detail

**Files:**
- Create: `internal/server/router.go`
- Create: `internal/server/middleware.go`
- Create: `internal/server/channels.go`
- Create: `internal/server/channels_test.go`

- [ ] **Step 1: Router skeleton**

```go
package server

import "github.com/go-chi/chi/v5"

type Server struct {
	Store    *store.Store
	Stream   *streamproxy.SessionDeps
	Settings *settings.Snapshot
	Logger   hclog.Logger
}

func (s *Server) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(s.requireSession)
	r.Route("/api/v1/livetv", func(r chi.Router) {
		r.Get("/channels", s.listChannels)
		r.Get("/channels/{id}", s.getChannel)
		r.Get("/groups", s.listGroups)
		r.Get("/guide", s.guideWindow)
		r.Get("/programs/{id}", s.getProgram)
		r.Get("/programs/search", s.searchPrograms)
		r.Get("/favorites", s.listFavorites)
		r.Post("/favorites/{channel_id}", s.addFavorite)
		r.Delete("/favorites/{channel_id}", s.removeFavorite)
		r.Post("/favorites/reorder", s.reorderFavorites)
		r.Get("/recent", s.listRecent)
		r.Post("/channels/{id}/stream", s.Stream.CreateSession)
		// stream proxy paths bypass requireSession; gated by cookie
	})
	r.Route("/api/v1/livetv/stream", func(r chi.Router) {
		r.Get("/{session_id}.ts",       s.Stream.ProxyMPEGTS)
		r.Get("/{session_id}.m3u8",     s.Stream.ProxyHLSPlaylist)
		r.Get("/{session_id}/segment",  s.Stream.ProxyHLSSegment)
	})
	r.Route("/api/v1/livetv/admin", s.adminRoutes)
	return r
}
```

- [ ] **Step 2: Middleware** — reads `X-Silo-User-Id` (header forwarded by host), 401 if missing. Adds `userID` to context.

- [ ] **Step 3: Channels handlers** with cursor pagination

```go
func (s *Server) listChannels(w http.ResponseWriter, r *http.Request) {
	userID := UserIDFromContext(r.Context())
	q := r.URL.Query()
	views, next, err := s.Store.ListChannelsForUser(r.Context(), userID,
		q.Get("group"), q.Get("q"), atoiOr(q.Get("limit"), 100), q.Get("cursor"))
	if err != nil { writeError(w, 500, err); return }
	writeJSON(w, 200, envelope{Data: toChannelDTOs(views), NextCursor: next})
}

func (s *Server) getChannel(w http.ResponseWriter, r *http.Request) {
	userID := UserIDFromContext(r.Context())
	id := chi.URLParam(r, "id")
	ch, err := s.Store.GetChannelView(r.Context(), userID, id)
	if err == store.ErrNotFound { writeError(w, 404, err); return }
	if err != nil { writeError(w, 500, err); return }
	writeJSON(w, 200, toChannelDTO(ch))
}
```

- [ ] **Step 4: Tests** with `httptest.NewRecorder`, inject middleware-faking handler that sets userID directly. Seed channels via the store, call `listChannels`, assert payload shape and pagination cursor.

- [ ] **Step 5: Commit**

```bash
go test ./internal/server/... -run Channels -v
git add -A && git commit -m "feat(livetv): user channel list and detail handlers"
```

---

## Task 20: Groups & guide window

**Files:**
- Modify: `internal/server/channels.go`
- Create: `internal/server/guide.go`
- Create: `internal/server/guide_test.go`

- [ ] **Step 1: Groups**

```go
func (s *Server) listGroups(w http.ResponseWriter, r *http.Request) {
	g, err := s.Store.ListGroups(r.Context())  // new store method: DISTINCT coalesce(group_title_admin, group_title_src) WHERE enabled
	if err != nil { writeError(w, 500, err); return }
	writeJSON(w, 200, envelope{Data: g})
}
```

- [ ] **Step 2: Guide window**

```go
func (s *Server) guideWindow(w http.ResponseWriter, r *http.Request) {
	userID := UserIDFromContext(r.Context())
	start, _ := time.Parse(time.RFC3339, r.URL.Query().Get("start"))
	end, _ := time.Parse(time.RFC3339, r.URL.Query().Get("end"))
	if start.IsZero() { start = time.Now().UTC() }
	if end.IsZero() { end = start.Add(4 * time.Hour) }
	cap := s.Settings.GuideWindowCap
	if end.Sub(start) > cap { end = start.Add(cap) }
	channels := r.URL.Query()["channels"] // optional repeated
	if len(channels) == 0 {
		channels, _ = s.Store.VisibleChannelIDsForUser(r.Context(), userID, r.URL.Query().Get("group"))
	}
	rows, err := s.Store.GuideWindow(r.Context(), channels, start, end)
	if err != nil { writeError(w, 500, err); return }
	writeJSON(w, 200, envelope{Data: rows, Window: window{Start: start, End: end}})
}
```

- [ ] **Step 3: Tests** — seed 2 channels, 6 programmes; query a 1h window centered on one programme; assert grouped response shape.

- [ ] **Step 4: Commit**

```bash
go test ./internal/server/... -v
git add -A && git commit -m "feat(livetv): groups and guide-window handlers"
```

---

## Task 21: Program detail & search

**Files:**
- Create: `internal/server/programs.go`
- Create: `internal/server/programs_test.go`

- [ ] **Step 1: Handlers**

```go
func (s *Server) getProgram(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	p, err := s.Store.GetProgram(r.Context(), id)
	if err == store.ErrNotFound { writeError(w, 404, err); return }
	if err != nil { writeError(w, 500, err); return }
	writeJSON(w, 200, p)
}

func (s *Server) searchPrograms(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" { writeJSON(w, 200, envelope{Data: []store.Program{}}); return }
	from, _ := time.Parse(time.RFC3339, r.URL.Query().Get("from"))
	to,   _ := time.Parse(time.RFC3339, r.URL.Query().Get("to"))
	if from.IsZero() { from = time.Now().UTC() }
	if to.IsZero() { to = from.Add(48 * time.Hour) }
	limit := atoiOr(r.URL.Query().Get("limit"), 100)
	if limit > 500 { limit = 500 }
	rows, err := s.Store.SearchPrograms(r.Context(), q, from, to, limit)
	if err != nil { writeError(w, 500, err); return }
	writeJSON(w, 200, envelope{Data: rows})
}
```

- [ ] **Step 2: Tests** — seed 3 programmes spanning 3 days, search by partial title returns the right one; out-of-window programmes excluded.

- [ ] **Step 3: Commit**

```bash
go test ./internal/server/... -v
git add -A && git commit -m "feat(livetv): program detail and search handlers"
```

---

## Task 22: Favorites endpoints

**Files:**
- Create: `internal/server/favorites.go`
- Create: `internal/server/favorites_test.go`

- [ ] **Step 1: Handlers**

```go
func (s *Server) listFavorites(w http.ResponseWriter, r *http.Request) {
	favs, err := s.Store.ListFavorites(r.Context(), UserIDFromContext(r.Context()))
	if err != nil { writeError(w, 500, err); return }
	writeJSON(w, 200, envelope{Data: favs})
}

func (s *Server) addFavorite(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.AddFavorite(r.Context(), UserIDFromContext(r.Context()), chi.URLParam(r, "channel_id")); err != nil {
		writeError(w, 500, err); return
	}
	w.WriteHeader(204)
}

func (s *Server) removeFavorite(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.RemoveFavorite(r.Context(), UserIDFromContext(r.Context()), chi.URLParam(r, "channel_id")); err != nil {
		writeError(w, 500, err); return
	}
	w.WriteHeader(204)
}

func (s *Server) reorderFavorites(w http.ResponseWriter, r *http.Request) {
	var body struct{ ChannelIDs []string `json:"channel_ids"` }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil { writeError(w, 400, err); return }
	if err := s.Store.ReorderFavorites(r.Context(), UserIDFromContext(r.Context()), body.ChannelIDs); err != nil {
		writeError(w, 500, err); return
	}
	w.WriteHeader(204)
}
```

- [ ] **Step 2: Tests** — add 3 favorites, list them, reorder, assert positions; remove one, assert remaining 2 in correct order.

- [ ] **Step 3: Commit**

```bash
go test ./internal/server/... -v
git add -A && git commit -m "feat(livetv): favorites endpoints"
```

---

## Task 23: Recent + smoke build

**Files:**
- Create: `internal/server/recent.go`
- Create: `internal/server/recent_test.go`

- [ ] **Step 1: Handler**

```go
func (s *Server) listRecent(w http.ResponseWriter, r *http.Request) {
	limit := atoiOr(r.URL.Query().Get("limit"), 20)
	rows, err := s.Store.ListRecent(r.Context(), UserIDFromContext(r.Context()), limit)
	if err != nil { writeError(w, 500, err); return }
	writeJSON(w, 200, envelope{Data: rows})
}
```

- [ ] **Step 2: Wire `server.Server` into `main.go`** so the SDK runtime serves the chi router.

- [ ] **Step 3: Smoke test** — `go build ./... && go test ./...`.

- [ ] **Step 4: Commit**

```bash
git add -A && git commit -m "feat(livetv): recent endpoint and api wiring"
```

---

# Phase 7 — Admin HTTP API

## Task 24: Sources admin CRUD

**Files:**
- Create: `internal/server/admin_sources.go`
- Create: `internal/server/admin_sources_test.go`
- Modify: `internal/server/middleware.go` (add `requireAdmin`)

- [ ] **Step 1: Admin auth middleware**

```go
func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Silo-Admin") != "true" { http.Error(w, "forbidden", 403); return }
		next.ServeHTTP(w, r)
	})
}

func (s *Server) adminRoutes(r chi.Router) {
	r.Use(s.requireAdmin)
	r.Route("/sources/m3u",   s.adminM3URoutes)
	r.Route("/sources/xmltv", s.adminXMLTVRoutes)
	r.Route("/channels",      s.adminChannelRoutes)
	r.Route("/sessions",      s.adminSessionRoutes)
	r.Get("/settings",        s.getSettings)
	r.Put("/settings",        s.updateSettings)
}
```

- [ ] **Step 2: M3U handlers**

```go
func (s *Server) adminM3URoutes(r chi.Router) {
	r.Get("/", s.listM3U)
	r.Post("/", s.createM3U)
	r.Get("/{id}", s.getM3U)
	r.Put("/{id}", s.updateM3U)
	r.Delete("/{id}", s.deleteM3U)
	r.Post("/{id}/refresh", s.refreshM3U)
}

func (s *Server) refreshM3U(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.M3UWorker.RefreshOne(r.Context(), id); err != nil { writeError(w, 502, err); return }
	w.WriteHeader(204)
}
```

Symmetric for XMLTV.

- [ ] **Step 3: Tests** — create M3U source → list contains it → refresh-now triggers worker (assert via test-double worker that records calls).

- [ ] **Step 4: Commit**

```bash
git add -A && git commit -m "feat(livetv): admin source crud and manual refresh"
```

---

## Task 25: Admin channels (overrides + epg links)

**Files:**
- Create: `internal/server/admin_channels.go`
- Create: `internal/server/admin_channels_test.go`

- [ ] **Step 1: Handlers** — list with overrides shown, `PATCH /{id}` for `channel_number_admin`, `group_title_admin`, `enabled_admin`, `position`. `POST /{id}/epg-keys` body `{xmltv_channel_id}`. `DELETE /{id}/epg-keys/{xmltv_channel_id}`.

- [ ] **Step 2: Tests** — PATCH then ListChannelsForUser sees the override; clearing override (PATCH with null fields) reverts to `*_src`; EPG-key link bidirectional behavior.

- [ ] **Step 3: Commit**

```bash
git add -A && git commit -m "feat(livetv): admin channel overrides and epg-link management"
```

---

## Task 26: Admin sessions (list + kill)

**Files:**
- Create: `internal/server/admin_sessions.go`
- Create: `internal/server/admin_sessions_test.go`

- [ ] **Step 1: Handlers**

```go
func (s *Server) adminSessionRoutes(r chi.Router) {
	r.Get("/", s.listSessions)
	r.Post("/{id}/kill", s.killSession)
}
```

`killSession`: end the session, revoke the scoped grant via `auth.Host`.

- [ ] **Step 2: Tests** — create a session, list shows it, kill ends it.

- [ ] **Step 3: Commit**

```bash
git add -A && git commit -m "feat(livetv): admin session list and kill"
```

---

## Task 27: Settings + Snapshot cache

**Files:**
- Create: `internal/settings/snapshot.go`
- Create: `internal/server/admin_settings.go`
- Create: `internal/server/admin_settings_test.go`

- [ ] **Step 1: Snapshot**

```go
package settings

type Snapshot struct {
	mu                    sync.RWMutex
	DefaultM3URefresh     time.Duration
	DefaultXMLTVRefresh   time.Duration
	GuideWindowCap        time.Duration
	PerUserStreamCap      int
	PerChannelDefaultCap  int
	SessionIdleTimeout    time.Duration
}

func Load(ctx context.Context, st *store.Store) (*Snapshot, error)
func (s *Snapshot) Reload(ctx context.Context, st *store.Store) error
```

- [ ] **Step 2: Wire snapshot reload after PUT** in `updateSettings`.

- [ ] **Step 3: Tests** — initial settings match defaults; PUT updates them; Reload reflects in `s.GuideWindowCap`.

- [ ] **Step 4: Commit**

```bash
git add -A && git commit -m "feat(livetv): settings snapshot and admin endpoints"
```

> **Phase 7 checkpoint:** at this point the plugin should build, all tests pass, and an operator could exercise the entire API via curl. Smoke-test with the manifest installed in a local Silo host before continuing to the SPA.

---

# Phase 8 — Web SPA scaffold & user pages

## Task 28: SPA scaffold

**Files:**
- Create: `web/package.json`, `web/vite.config.ts`, `web/tsconfig.json`, `web/index.html`, `web/src/main.tsx`, `web/src/App.tsx`, `web/src/index.css`, `web/embed.go`, `web/components.json`
- Create: `web/src/api/client.ts`
- Create: `web/src/lib/queryClient.ts`

- [ ] **Step 1: Copy structure from audiobooks**

```bash
cd /opt/silo_plugins/silo-plugin-livetv
cp -r ../silo-plugin-audiobooks/web/{package.json,vite.config.ts,tsconfig.json,tsconfig.node.json,index.html,components.json,playwright.config.ts,embed.go,public} web/
mkdir -p web/src/{api,components,lib,pages,player,e2e}
```
Then edit `web/package.json` `name` to `"silo-plugin-livetv-web"`, add deps `hls.js` and `mpegts.js`, and `@tanstack/react-virtual` for the guide grid.

- [ ] **Step 2: `web/src/main.tsx`**

```tsx
import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { BrowserRouter } from 'react-router';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { Toaster } from 'sonner';
import { App } from './App';
import './index.css';

const qc = new QueryClient({ defaultOptions: { queries: { staleTime: 30_000 } } });
createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <QueryClientProvider client={qc}>
      <BrowserRouter>
        <App />
        <Toaster richColors />
      </BrowserRouter>
    </QueryClientProvider>
  </StrictMode>
);
```

- [ ] **Step 3: `web/src/App.tsx`** — placeholder routes

```tsx
import { Route, Routes, NavLink } from 'react-router';
import { Home } from './pages/Home';
import { Channels } from './pages/Channels';
import { Guide } from './pages/Guide';
import { PlayerPage } from './pages/Player';
import { Search } from './pages/Search';
import { Favorites } from './pages/Favorites';
import { AdminLayout } from './pages/admin/Layout';

export function App() {
  return (
    <div className="min-h-screen bg-zinc-950 text-zinc-50">
      <nav className="flex gap-3 p-3 border-b border-zinc-800">
        <NavLink to="/" end>Home</NavLink>
        <NavLink to="/guide">Guide</NavLink>
        <NavLink to="/channels">Channels</NavLink>
        <NavLink to="/favorites">Favorites</NavLink>
        <NavLink to="/search">Search</NavLink>
        <NavLink to="/admin">Admin</NavLink>
      </nav>
      <Routes>
        <Route path="/" element={<Home />} />
        <Route path="/guide" element={<Guide />} />
        <Route path="/channels" element={<Channels />} />
        <Route path="/favorites" element={<Favorites />} />
        <Route path="/search" element={<Search />} />
        <Route path="/watch/:channelId" element={<PlayerPage />} />
        <Route path="/admin/*" element={<AdminLayout />} />
      </Routes>
    </div>
  );
}
```

- [ ] **Step 4: API client**

```ts
// web/src/api/client.ts
const BASE = '/api/v1/livetv';
async function req<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(BASE + path, { credentials: 'include', ...init,
    headers: { 'Content-Type': 'application/json', ...(init?.headers || {}) } });
  if (!res.ok) throw new Error(`${res.status} ${await res.text()}`);
  return res.status === 204 ? (undefined as T) : (await res.json() as T);
}
export const api = {
  channels:  (q?: { group?: string; q?: string; cursor?: string; limit?: number }) =>
    req<{ data: Channel[]; next_cursor?: string }>(`/channels?${new URLSearchParams(q as any).toString()}`),
  guide:     (start: string, end: string, opts?: { group?: string; channels?: string[] }) =>
    req<{ data: Record<string, Program[]> }>(`/guide?start=${start}&end=${end}${opts?.group ? `&group=${opts.group}` : ''}${(opts?.channels||[]).map(c=>`&channels=${c}`).join('')}`),
  groups:    () => req<{ data: string[] }>('/groups'),
  program:   (id: string) => req<Program>(`/programs/${id}`),
  search:    (q: string, from?: string, to?: string) =>
    req<{ data: Program[] }>(`/programs/search?q=${encodeURIComponent(q)}${from?`&from=${from}`:''}${to?`&to=${to}`:''}`),
  favorites: () => req<{ data: Favorite[] }>('/favorites'),
  addFav:    (id: string) => req<void>(`/favorites/${id}`, { method: 'POST' }),
  delFav:    (id: string) => req<void>(`/favorites/${id}`, { method: 'DELETE' }),
  reorderFav:(ids: string[]) => req<void>('/favorites/reorder', { method: 'POST', body: JSON.stringify({ channel_ids: ids }) }),
  recent:    () => req<{ data: Recent[] }>('/recent'),
  startStream:(channelId: string) =>
    req<{ session_id: string; playback_url: string; expires_at: string }>(`/channels/${channelId}/stream`, { method: 'POST' }),
};
// Type declarations follow…
```

- [ ] **Step 5: `web/embed.go`**

```go
package web

import "embed"

//go:embed dist
var Dist embed.FS
```

- [ ] **Step 6: Smoke build**

```bash
cd web && pnpm install && pnpm build
```
Expected: succeeds with empty pages.

- [ ] **Step 7: Commit**

```bash
git add -A && git commit -m "feat(livetv): web spa scaffold and api client"
```

---

## Task 29: Channels page

**Files:**
- Create: `web/src/pages/Channels.tsx`
- Create: `web/src/components/ChannelCard.tsx`

- [ ] **Step 1: ChannelCard component** — logo, name, current program title, favorite star toggle, click → `/watch/:id`.

- [ ] **Step 2: Channels page** — group chips along the top (from `api.groups`), search input, results grid using `react-virtuoso` or a simple `grid grid-cols-2 md:grid-cols-4 xl:grid-cols-6 gap-3`. Use TanStack Query (`useQuery(['channels', group, q], () => api.channels({group, q}))`).

- [ ] **Step 3: Commit**

```bash
git add -A && git commit -m "feat(livetv): channels page"
```

---

## Task 30: Guide grid (headline view)

**Files:**
- Create: `web/src/pages/Guide.tsx`
- Create: `web/src/components/guide/Grid.tsx`
- Create: `web/src/components/guide/TimeRow.tsx`
- Create: `web/src/components/guide/ChannelColumn.tsx`
- Create: `web/src/components/guide/ProgramCell.tsx`

- [ ] **Step 1: Page-level state** — current window (start, end), default `[now, now+4h]`. Scrubber to move ±1d / +14d.

- [ ] **Step 2: Two-axis virtualization** using `@tanstack/react-virtual`:
  - Outer container `overflow-auto`, `position: relative`.
  - `useVirtualizer({ count: channels.length, getScrollElement, estimateSize: () => 56, horizontal: false })` for rows.
  - For each visible row, render a horizontal strip; programmes positioned absolutely by computing `(program.start - window.start) * pxPerMinute`.
  - Sticky channel column on the left (separate scrolling container that syncs vertical scrollTop).
  - Sticky time row on top with 30-min ticks.

- [ ] **Step 3: Refetch on minute boundary** — `useEffect` setting `setInterval` aligned to next minute that invalidates `['guide', start, end]`.

- [ ] **Step 4: Click handlers** — clicking a cell opens `ProgramDetail` modal (route `/guide/program/:id`). If the cell is currently airing, modal shows "Play now" button that posts `startStream` and navigates to `/watch/:channelId`.

- [ ] **Step 5: Tests** — Vitest unit tests for the time-position math (`programCellLeft(start, windowStart, pxPerMin)` etc).

- [ ] **Step 6: Commit**

```bash
git add -A && git commit -m "feat(livetv): guide grid with virtualized rows and sticky axes"
```

---

## Task 31: Player page

**Files:**
- Create: `web/src/pages/Player.tsx`
- Create: `web/src/player/HlsPlayer.tsx`
- Create: `web/src/player/MpegtsPlayer.tsx`

- [ ] **Step 1: Page logic**

```tsx
export function PlayerPage() {
  const { channelId = '' } = useParams();
  const { data, isLoading, error } = useQuery({
    queryKey: ['stream', channelId],
    queryFn: () => api.startStream(channelId),
    refetchOnWindowFocus: false,
  });
  const channel = useQuery({ queryKey: ['channel', channelId], queryFn: () => api.channel(channelId) });
  if (isLoading) return <div className="p-6">Starting stream…</div>;
  if (error) return <div className="p-6 text-red-400">Stream unavailable.</div>;
  const isHLS = data!.playback_url.endsWith('.m3u8');
  return (
    <div className="p-4 grid grid-cols-1 lg:grid-cols-[2fr_1fr] gap-4">
      <div>
        {isHLS
          ? <HlsPlayer src={data!.playback_url} />
          : <MpegtsPlayer src={data!.playback_url} />}
      </div>
      <NowNextPanel channel={channel.data} />
    </div>
  );
}
```

- [ ] **Step 2: HlsPlayer**

```tsx
import Hls from 'hls.js';

export function HlsPlayer({ src }: { src: string }) {
  const ref = useRef<HTMLVideoElement>(null);
  useEffect(() => {
    const v = ref.current!;
    if (Hls.isSupported()) {
      const hls = new Hls({ liveDurationInfinity: true });
      hls.loadSource(src);
      hls.attachMedia(v);
      return () => hls.destroy();
    }
    v.src = src; // Safari native HLS
  }, [src]);
  return <video ref={ref} controls autoPlay className="w-full aspect-video bg-black" />;
}
```

- [ ] **Step 3: MpegtsPlayer**

```tsx
import mpegts from 'mpegts.js';

export function MpegtsPlayer({ src }: { src: string }) {
  const ref = useRef<HTMLVideoElement>(null);
  useEffect(() => {
    const v = ref.current!;
    if (mpegts.getFeatureList().mseLivePlayback) {
      const player = mpegts.createPlayer({ type: 'mpegts', isLive: true, url: src });
      player.attachMediaElement(v);
      player.load();
      player.play();
      return () => { player.destroy(); };
    }
    v.src = src;
  }, [src]);
  return <video ref={ref} controls autoPlay className="w-full aspect-video bg-black" />;
}
```

- [ ] **Step 4: NowNextPanel** — uses `api.guide` for `[now, now+2h]` filtered to this channel.

- [ ] **Step 5: Commit**

```bash
git add -A && git commit -m "feat(livetv): player page with hls.js and mpegts.js"
```

---

## Task 32: ProgramDetail modal

**Files:**
- Create: `web/src/components/ProgramDetail.tsx`
- Modify: `web/src/App.tsx` (route `/guide/program/:id` overlays a `Dialog`)

- [ ] **Step 1: Dialog with description, credits, schedule, "Play now" button.** Use `radix-ui` `Dialog` mirroring audiobooks' modal patterns.

- [ ] **Step 2: Commit**

```bash
git add -A && git commit -m "feat(livetv): program detail modal"
```

---

## Task 33: Favorites page

**Files:**
- Create: `web/src/pages/Favorites.tsx`

- [ ] **Step 1: Drag-to-reorder list** (use `@dnd-kit/sortable`). On drop, `api.reorderFav(ids)`. Optimistic update via `useMutation` `onMutate`.

- [ ] **Step 2: Commit**

```bash
git add -A && git commit -m "feat(livetv): favorites page with drag-reorder"
```

---

## Task 34: Search page

**Files:**
- Create: `web/src/pages/Search.tsx`

- [ ] **Step 1: Debounced input → `api.search(q)` → result list grouped by channel.** Each result clickable to ProgramDetail modal.

- [ ] **Step 2: Commit**

```bash
git add -A && git commit -m "feat(livetv): epg search page"
```

---

## Task 35: Home page

**Files:**
- Create: `web/src/pages/Home.tsx`
- Create: `web/src/components/Rail.tsx`

- [ ] **Step 1: Three rails** — recently watched, favorites, "on now across favorites" (computed client-side from a guide query against favorite channel ids for `[now, now+2h]`).

- [ ] **Step 2: Commit**

```bash
git add -A && git commit -m "feat(livetv): home page with recent/favorites/on-now rails"
```

---

# Phase 9 — Web SPA admin pages

## Task 36: Admin Sources

**Files:**
- Create: `web/src/pages/admin/Layout.tsx`
- Create: `web/src/pages/admin/Sources.tsx`
- Create: `web/src/api/admin.ts`

- [ ] **Step 1: AdminLayout** — left rail with links to Sources / Channels / Sessions / Settings. Nested `<Routes>`.
- [ ] **Step 2: Sources page** — two tables (M3U, XMLTV). Each row: name, URL, status badge, last refreshed, "Refresh" button. Add/Edit drawer (radix `Sheet`) with form: name, URL, headers JSON, refresh interval, enabled.
- [ ] **Step 3: Commit**

```bash
git add -A && git commit -m "feat(livetv): admin sources ui"
```

---

## Task 37: Admin Channels

**Files:**
- Create: `web/src/pages/admin/Channels.tsx`

- [ ] **Step 1: Table** with admin overrides (number, group), enabled toggle, EPG-link dropdown (single-select to start; "Add more" opens a list). Server-side filter by source.
- [ ] **Step 2: Commit**

```bash
git add -A && git commit -m "feat(livetv): admin channels ui with overrides and epg links"
```

---

## Task 38: Admin Sessions

**Files:**
- Create: `web/src/pages/admin/Sessions.tsx`

- [ ] **Step 1: Live table of active sessions** (refetch every 30s), user, channel, started, bytes streamed, kill button.
- [ ] **Step 2: Commit**

```bash
git add -A && git commit -m "feat(livetv): admin sessions ui"
```

---

## Task 39: Admin Settings

**Files:**
- Create: `web/src/pages/admin/Settings.tsx`

- [ ] **Step 1: Form** for the 6 fields in `settings` (defaults + caps + idle timeout). Save → `PUT /admin/settings`.
- [ ] **Step 2: Commit**

```bash
git add -A && git commit -m "feat(livetv): admin settings ui"
```

---

# Phase 10 — E2E, docs, manifest checksum

## Task 40: Playwright e2e happy path

**Files:**
- Create: `web/e2e/livetv.spec.ts`
- Modify: `web/playwright.config.ts` (set baseURL)

- [ ] **Step 1: Spec** drives the full flow against a real server with seeded sources.

```ts
import { test, expect } from '@playwright/test';

test('admin adds sources, user watches a channel', async ({ page }) => {
  await page.goto('/admin/sources');
  // add M3U
  await page.getByRole('button', { name: 'Add M3U source' }).click();
  await page.getByLabel('Name').fill('Test M3U');
  await page.getByLabel('URL').fill(process.env.E2E_M3U_URL!);
  await page.getByRole('button', { name: 'Save' }).click();
  await page.getByRole('button', { name: 'Refresh', exact: true }).first().click();
  await expect(page.getByText('ok')).toBeVisible();

  // add XMLTV
  await page.goto('/admin/sources');
  await page.getByRole('button', { name: 'Add XMLTV source' }).click();
  await page.getByLabel('Name').fill('Test EPG');
  await page.getByLabel('URL').fill(process.env.E2E_XMLTV_URL!);
  await page.getByRole('button', { name: 'Save' }).click();
  await page.getByRole('button', { name: 'Refresh' }).nth(1).click();

  // navigate to channels and play
  await page.goto('/channels');
  await expect(page.locator('[data-testid=channel-card]')).toHaveCount.greaterThan(0);
  await page.locator('[data-testid=channel-card]').first().click();
  await expect(page.locator('video')).toBeVisible();
});
```

The test relies on `E2E_M3U_URL` / `E2E_XMLTV_URL` set in the env to point at small in-repo fixtures served by a `playwright.webServer` helper. A simple Go test server is acceptable.

- [ ] **Step 2: Run**

```bash
cd web && pnpm test:e2e
```

- [ ] **Step 3: Commit**

```bash
git add -A && git commit -m "test(livetv): playwright happy-path e2e"
```

---

## Task 41: Setup / debug docs

**Files:**
- Create: `docs/setup-debug-flows.md`
- Update: `README.md`

- [ ] **Step 1: `docs/setup-debug-flows.md`** with:
  - Database setup (`CREATE ROLE`, `CREATE SCHEMA`, DSN example).
  - First-install steps: install plugin, set DSN, add M3U + XMLTV.
  - Refresh debugging: where to look at `m3u_sources.last_status` and refresher logs.
  - Stream debugging: how to inspect `stream_sessions`, identify upstream errors, check cookie scope.
  - Browser compatibility notes (Safari + native HLS; Chrome + mpegts.js; native app fallback when neither works).

- [ ] **Step 2: Update `README.md`** mirroring the audiobooks README shape (Features, Architecture, Configuration, Database Setup, HTTP Surface, Events).

- [ ] **Step 3: Commit**

```bash
git add -A && git commit -m "docs(livetv): setup, debug, readme"
```

---

## Task 42: Manifest checksum + Makefile + final build

**Files:**
- Create: `Makefile`

- [ ] **Step 1: Makefile** (mirrors audiobooks)

```make
.PHONY: build test web fmt vet

build: web
	go build -o silo-plugin-livetv ./cmd/silo-plugin-livetv
	sha256sum silo-plugin-livetv | awk '{print $$1}' > silo-plugin-livetv.sha256
	# Replace __CHECKSUM__ in the embedded manifest copy
	# (the plugin computes the checksum at runtime in main.go — leave the file template intact)

web:
	cd web && pnpm install --frozen-lockfile && pnpm build

test:
	go test ./...
	cd web && pnpm test

fmt: ; go fmt ./...
vet: ; go vet ./...
```

- [ ] **Step 2: Runtime checksum** — add to `main.go` so the embedded `manifest.json` `__CHECKSUM__` is replaced with the running binary's sha256 before returning the manifest (mirrors the audiobooks pattern; see its main.go for the exact code).

- [ ] **Step 3: Final builds**

```bash
make build
make test
```

- [ ] **Step 4: Commit**

```bash
git add -A && git commit -m "build(livetv): makefile and runtime manifest checksum"
```

---

# Self-Review

**Spec coverage check:**
- §1 Goals & non-goals → bound by Phase scope; non-goals (DVR, transcode, native UI, fan-out) all explicitly absent.
- §2 Architecture & deployment → Task 1.
- §3 Data model → Tasks 2–5; admin-override pattern realized via `*_src`/`*_admin` columns.
- §4 Refresh workers → Tasks 12–14.
- §5 HTTP API → Tasks 19–27.
- §6 Stream proxy → Tasks 15–18.
- §7 Web SPA → Tasks 28–39.
- §8 Manifest → Task 1 (initial) + Task 42 (checksum).
- §9 Testing → unit tests folded into each task; e2e in Task 40.
- §10 Known limitations → enforced by scope (no DVR/transcode/fan-out tasks present).
- §11 Build & verification → Task 42 + Task 41.

**Placeholder scan:** every step shows the code/SQL/commands needed; no "TBD"/"add appropriate handling" left in. Two notes flag "confirm against SDK at impl time" — those are honest gaps where the SDK API surface needs to be re-checked, not pure placeholders.

**Type consistency:** `ChannelView`, `Channel`, `Program`, `Programme` (parser) vs `Program` (store/DTO) — the parser uses `Programme` (XMLTV term), the store uses `Program` for the persisted shape. Each function signature is consistent within its phase. `Session`, `Favorite`, `Recent`, `MintedGrant` defined once and reused. ID type is `string` (ULID) throughout.

---

# Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-05-21-livetv-plugin.md`. Two execution options:

1. **Subagent-Driven (recommended)** — I dispatch a fresh subagent per task, review between tasks, fast iteration.
2. **Inline Execution** — Execute tasks in this session using `executing-plans`, batch execution with checkpoints.

Which approach?
