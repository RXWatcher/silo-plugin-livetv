# Live TV Portal for Continuum

`continuum.livetv` is Continuum's user-facing live TV portal. It ingests
IPTV M3U playlists and XMLTV electronic program guides, presents a
channel grid and guide, proxies streams to clients, and tracks per-user
favorites and recent channels.

Install this plugin when you want a single live TV experience in
Continuum that points at one or more operator-configured M3U + XMLTV
sources.

## Detailed Operations Docs

- [Setup, debugging, and communication flows](docs/setup-debug-flows.md)

## Features

- M3U source ingestion with periodic refresh (default every 6 hours).
- XMLTV guide ingestion with periodic refresh (default every 3 hours).
- Channel grid, channel detail, favorites, recents, and EPG search.
- Virtualized program guide grid with sticky time axis.
- Per-user and global concurrency caps with idle-session reaping.
- Stream proxy with scoped session grants; HLS via `hls.js` and
  MPEG-TS via `mpegts.js` in the browser, native HLS in Safari.
- Admin UI for sources, per-channel overrides, EPG link keys, live
  sessions, and runtime settings.

## Architecture

The portal owns its own Postgres schema and serves both the
customer-facing SPA and the admin SPA from a single embedded asset
bundle. Stream traffic flows through a thin proxy that mints scoped
session cookies against the configured M3U upstream.

The plugin is a single Go binary that bundles:

- the chi-based HTTP router (user, admin, and stream-byte routes),
- the M3U and XMLTV parsers,
- the refresh workers and scheduler,
- the stream proxy,
- the embedded React 19 SPA.

## Configuration

| Key | Required | Description |
|---|---|---|
| `database_url` | yes | Postgres DSN using the `livetv` schema. |

Example DSN:

```text
postgres://plugin_livetv:password@postgres:5432/continuum?search_path=livetv&sslmode=disable
```

All other settings (refresh intervals, idle timeouts, concurrency caps)
live in `/admin/settings` and are editable at runtime without a
restart.

## Database Setup

```sql
CREATE ROLE plugin_livetv WITH LOGIN PASSWORD '<chosen>';
CREATE SCHEMA livetv AUTHORIZATION plugin_livetv;
GRANT CONNECT ON DATABASE continuum TO plugin_livetv;
```

The plugin applies its migrations at startup.

## Provider Setup

After installing the portal:

1. Open the Live TV admin UI -> `/admin/sources`.
2. Add an M3U source by URL. Click "Refresh"; wait for `last_status='ok'`.
3. Optionally add an XMLTV source for the EPG. Refresh and wait for ok.
4. Visit `/admin/channels` to set per-channel `tvg_id` overrides or
   extra EPG link keys for channels whose M3U id does not match the
   XMLTV id.
5. Visit `/channels` to confirm channels populated and tune one to
   verify playback.

For full setup, debugging, and operational flows see
[`docs/setup-debug-flows.md`](docs/setup-debug-flows.md).

## HTTP Surface

| Route | Access | Purpose |
|---|---|---|
| `/api/v1/livetv/channels` | authenticated | Channel list. |
| `/api/v1/livetv/channels/{id}` | authenticated | Channel detail. |
| `/api/v1/livetv/groups` | authenticated | Channel groups. |
| `/api/v1/livetv/guide` | authenticated | Program guide window. |
| `/api/v1/livetv/programs/{id}` | authenticated | Program detail. |
| `/api/v1/livetv/programs/search` | authenticated | EPG search. |
| `/api/v1/livetv/favorites*` | authenticated | Favorites CRUD + reorder. |
| `/api/v1/livetv/recent` | authenticated | Recent channels. |
| `/api/v1/livetv/channels/{id}/stream` | authenticated | Mint session. |
| `/api/v1/livetv/stream/{id}.{ts,m3u8}` | session cookie | Stream bytes. |
| `/api/v1/livetv/admin/*` | admin | Sources, channels, sessions, settings. |
| `/healthz` | public | Liveness probe (204). |
| `/*` | authenticated | Live TV SPA assets. |

## Build And Test

```bash
make test     # go test ./... and pnpm vitest
make build    # builds the SPA, the binary, and writes a .sha256 file
```

Or run individual steps:

```bash
go test ./...
cd web && pnpm install && pnpm run build
go build -o continuum-plugin-livetv ./cmd/continuum-plugin-livetv
```

## Known Limitations

- No per-channel concurrency cap (only per-user and global).
- No transcoding; clients must demux the upstream natively.
- No automatic XMLTV-to-channel name fuzzy match; mismatches require
  manual `tvg_id` overrides in `/admin/channels`.
- Single-tenant per installation; the host's user-id header is the only
  scope.
