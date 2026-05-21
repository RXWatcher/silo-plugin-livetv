# Live TV Plugin — Design Spec

**Status:** draft for review
**Date:** 2026-05-21
**Plugin name:** `continuum-plugin-livetv`
**Scope:** Watch-only MVP (no DVR). IPTV (M3U) channel source + XMLTV EPG + auth-gated stream proxy + web SPA with full guide / search / favorites.

---

## 1. Goals and non-goals

### Goals

- One self-contained Continuum plugin that delivers a Jellyfin-equivalent Live TV experience in the web UI: channel list, two-axis EPG grid, program detail, EPG search, per-user favorites, "now / next" surfacing.
- Source channels from one or more admin-configured M3U URLs.
- Source EPG from one or more admin-configured XMLTV URLs, auto-linked to channels by `tvg-id` with manual override.
- Gate playback bytes through Continuum's auth model so guests, scoped grants, and per-user concurrency caps are enforceable. No FFmpeg dependency.
- A stable, documented REST API under `/api/v1/livetv/...` so the native apps (`continuum-app`) can add a Live TV tab later without protocol change.

### Non-goals (MVP)

- No DVR / recording / series scheduling.
- No transcoding. Browser-side playback uses `hls.js` for HLS upstreams and `mpegts.js` for raw MPEG-TS. Sources that won't play in a given browser are documented as "use the native/TV app."
- No tuner-hardware support (HDHomeRun, Stalker portal). Channel ingest is M3U-only.
- No native app UI work in this project. Only the API surface is finalized so app teams can pick it up.
- No upstream connection fan-out (one viewer = one upstream connection). Documented limitation, revisit when DVR lands.

---

## 2. Architecture & deployment

A single new plugin `continuum-plugin-livetv` under `/opt/continuum_plugins/`, built with the existing Go SDK, same overall shape as `continuum-plugin-audiobooks`.

Capabilities the plugin advertises in its `manifest.json`:

- `http_routes.v1` — serves the SPA, user API, and admin API at the plugin's mounted root.
- `scheduled_task.v1` — registers `refresh_m3u_sources`, `refresh_xmltv_sources`, and `reap_idle_sessions`.
- `event_consumer.v1` — minimal: listens for `user_deleted` to clean favorites/sessions.

The plugin's own `database_url` (Postgres DSN) is the only config in the manifest form. Everything else (sources, refresh cadence, group defaults, concurrency caps) is admin-managed inside the plugin's own UI and stored in the plugin's Postgres schema.

```text
continuum-plugin-livetv/
├── cmd/continuum-plugin-livetv/        # main, manifest subcommand wiring
├── internal/
│   ├── m3u/                            # parser
│   ├── xmltv/                          # parser
│   ├── store/                          # sqlc-style typed Postgres access
│   ├── migrate/                        # SQL migrations
│   ├── refresh/                        # M3U + XMLTV scheduled jobs
│   ├── streamproxy/                    # scoped token validation, proxy handlers
│   ├── server/                         # HTTP handlers + routing
│   ├── auth/                           # session + scoped token glue
│   ├── runtime/                        # Runtime / capability servers
│   └── testutil/
├── web/                                # React 19 SPA, embedded via embed.go
├── docs/setup-debug-flows.md
├── Makefile
└── manifest.json (template)
```

### Database setup

Mirrors audiobooks:

```sql
CREATE ROLE plugin_livetv WITH LOGIN PASSWORD '<chosen>';
CREATE SCHEMA livetv AUTHORIZATION plugin_livetv;
GRANT CONNECT ON DATABASE continuum TO plugin_livetv;
```

DSN example:

```text
postgres://plugin_livetv:password@postgres:5432/continuum?search_path=livetv&sslmode=disable
```

Migrations apply at startup.

---

## 3. Data model

All tables in schema `livetv`. UUIDs for primary keys unless noted.

### `m3u_sources`

| col | type | notes |
|---|---|---|
| id | uuid | pk |
| name | text | admin label |
| url | text | source URL |
| http_headers | jsonb | optional User-Agent / Referer / cookies |
| enabled | bool | default true |
| refresh_interval | interval | default 6h, min 15m |
| last_refreshed_at | timestamptz | nullable |
| last_status | text | `ok` / `error: <message>` |
| etag | text | for conditional GET |
| last_modified | text | for conditional GET |

### `xmltv_sources`

Same shape as `m3u_sources` plus:

| col | type | notes |
|---|---|---|
| gzip | bool | default auto-detect from response |
| refresh_interval | interval | default 3h, min 15m |

### `channels`

| col | type | notes |
|---|---|---|
| id | uuid | pk |
| source_m3u_id | uuid | fk |
| source_channel_id | text | tvg-id if present, else slug(name + url hash) |
| display_name | text | from `tvg-name` / EXTINF title |
| channel_number_src | text | from `tvg-chno`; refresh overwrites |
| channel_number_admin | text | admin override; refresh never touches |
| logo_url | text | from `tvg-logo`; nullable; refresh overwrites |
| group_title_src | text | from `group-title`; refresh overwrites |
| group_title_admin | text | admin override; refresh never touches |
| upstream_url | text | the stream URL from M3U |
| upstream_kind | text | `mpegts` / `hls` / `unknown` (probed on first stream) |
| attrs | jsonb | raw EXTINF kv for forward compat |
| enabled_src | bool | true if present in last refresh |
| enabled_admin | bool | nullable; when non-null, takes precedence over `enabled_src` |
| position | int | admin reorder; refresh never touches |

The plugin always reads `coalesce(channel_number_admin, channel_number_src)` / `coalesce(group_title_admin, group_title_src)` / `coalesce(enabled_admin, enabled_src)` when surfacing channel data. A "clear override" admin action sets the `*_admin` column to NULL.

Unique on `(source_m3u_id, source_channel_id)`.

Indexes on `(enabled, group_title)` and `(enabled, display_name text_pattern_ops)` for filter / search.

### `channel_epg_keys`

Many-to-many link between `channels` and XMLTV channel IDs.

| col | type | notes |
|---|---|---|
| channel_id | uuid | fk |
| xmltv_channel_id | text | as appearing in `<channel id="...">` |
| auto_linked | bool | true if matched on tvg-id; false if set by admin |

Primary key `(channel_id, xmltv_channel_id)`.

### `programs`

| col | type | notes |
|---|---|---|
| id | uuid | pk |
| xmltv_channel_id | text | indexed |
| start_utc | timestamptz | indexed |
| stop_utc | timestamptz | indexed |
| title | text |
| sub_title | text |
| description | text |
| episode_num | text | original XMLTV `episode-num` text |
| season_num | int | parsed when episode-num is `xmltv_ns` |
| episode | int | parsed when episode-num is `xmltv_ns` |
| categories | text[] |
| rating | text |
| icon_url | text |
| original_air_date | date |

Composite index `(xmltv_channel_id, start_utc)` and `(start_utc, stop_utc)` for window queries.

Retention: keep rows where `stop_utc >= now() - 6h` and `start_utc <= now() + 14d`. Older rows pruned at the end of each XMLTV refresh.

### `program_credits`

| col | type | notes |
|---|---|---|
| program_id | uuid | fk cascade |
| kind | text | `actor` / `director` / `writer` / `presenter` / `guest` |
| name | text |
| position | int | preserves XMLTV order |

### `user_favorites`

| col | type | notes |
|---|---|---|
| user_id | uuid | fk to host user |
| channel_id | uuid | fk |
| position | int | for manual ordering |

Primary key `(user_id, channel_id)`.

### `user_recent`

| col | type | notes |
|---|---|---|
| user_id | uuid |
| channel_id | uuid |
| last_tuned_at | timestamptz |

Primary key `(user_id, channel_id)`.

### `stream_sessions`

| col | type | notes |
|---|---|---|
| id | uuid | pk |
| user_id | uuid |
| channel_id | uuid |
| scoped_grant_id | text | from `MintScopedStream` |
| started_at | timestamptz |
| last_byte_at | timestamptz | debounced update once per 5s |
| bytes_streamed | bigint |
| client_ip | inet |
| user_agent | text |
| ended_at | timestamptz | nullable; set when reaped/cancelled |
| end_reason | text | `client_disconnect`, `idle`, `admin_kill`, `upstream_error` |

### `settings` (single row)

| col | type |
|---|---|
| default_m3u_refresh | interval |
| default_xmltv_refresh | interval |
| guide_window_cap | interval |
| per_user_stream_cap | int |
| per_channel_default_cap | int |
| session_idle_timeout | interval |

---

## 4. Refresh / sync workers

Two scheduled tasks plus a session reaper, all registered via `scheduled_task.v1`. The plugin manages its own tickers internally; the host scheduler is used as a coarse heartbeat so an operator can pause/run-now from host admin if desired.

### `refresh_m3u_sources`

- Default cadence: every 6h. Per-source override honored.
- Manual "Refresh now" button per source and on save.
- For each enabled source:
  1. Conditional GET with `If-None-Match` / `If-Modified-Since` from stored values.
  2. On 304: update `last_refreshed_at`, skip.
  3. On 200: stream-parse `#EXTM3U`. For each `#EXTINF:` line, parse attributes (`tvg-id`, `tvg-name`, `tvg-logo`, `tvg-chno`, `group-title`, `tvg-shift`; other `catchup*` and `radio` attrs are ignored for MVP but preserved in `attrs` jsonb).
  4. Upsert by `(source_m3u_id, source_channel_id)`. Only the `*_src` columns and `display_name` / `logo_url` / `upstream_url` / `attrs` are written; `*_admin` columns and `position` are never touched by refresh.
  5. Channels seen in the previous refresh but absent now have `enabled_src` set to false — never hard-deleted, so favorites and history survive provider churn. `enabled_admin` (if set) still wins.
  6. Update `etag`, `last_modified`, `last_refreshed_at`, `last_status='ok'`.
- On any failure: `last_status='error: <message>'`, surfaced in admin UI. Channel rows unchanged.

### `refresh_xmltv_sources`

- Default cadence: every 3h. Per-source override honored.
- Conditional GET as above. If response is gzipped (header or content sniff), decompress.
- Streaming XML parse via `encoding/xml`:
  1. First pass collects `<channel>` ids (used to auto-link to M3U `tvg-id`).
  2. Second pass collects `<programme>` records into batches keyed by `xmltv_channel_id`.
- For each batch, in a single transaction per channel:
  1. Delete future programs for that `xmltv_channel_id` where `start_utc >= now()` (so a slipped schedule from a re-publish wins cleanly).
  2. Insert new programs and their credits.
- At end of refresh, prune `programs` where `stop_utc < now() - 6h`.
- Auto-link `channel_epg_keys` for any M3U channel whose `source_channel_id` matches an XMLTV `<channel id>` and which has no manual override row.

### `reap_idle_sessions`

- Runs every 1 minute.
- Marks `stream_sessions` rows with `last_byte_at < now() - settings.session_idle_timeout` as `ended_at=now()`, `end_reason='idle'`, revokes the scoped grant via host API.

All three workers are idempotent and safe to run concurrently with themselves; per-source mutex guards prevent overlapping refresh runs on the same source.

---

## 5. HTTP API surface

All paths are relative to the plugin's mounted root and ride Continuum's standard session auth. Response envelopes match audiobooks: `{ data: [...] }` for lists, flat object for detail. Pagination on collections is cursor-based: `?cursor=&limit=` (default 100, max 500).

### User API (`/api/v1/livetv/...`, authenticated)

| Method | Path | Purpose |
|---|---|---|
| GET | `/channels` | Channel list with current + next program, favorite flag, group filter, search |
| GET | `/channels/{id}` | Channel detail |
| GET | `/groups` | Distinct group titles for filter chips |
| GET | `/guide?start=&end=&channels=&group=&q=` | EPG window; server caps `end-start` at `settings.guide_window_cap` (default 24h) |
| GET | `/programs/{id}` | Program detail + credits |
| GET | `/programs/search?q=&from=&to=` | Title/description search across EPG window |
| GET | `/favorites` | Caller's favorites |
| POST | `/favorites/{channel_id}` | Add favorite |
| DELETE | `/favorites/{channel_id}` | Remove favorite |
| POST | `/favorites/reorder` | Bulk reorder |
| GET | `/recent` | Recently watched rail |
| POST | `/channels/{id}/stream` | Mints scoped grant, returns `playback_url` + `expires_at` |

### Stream proxy (auth-gated by scoped token, see Section 6)

| Method | Path | Purpose |
|---|---|---|
| GET | `/stream/{session_id}.ts` | Raw MPEG-TS proxy |
| GET | `/stream/{session_id}.m3u8` | HLS playlist proxy with URI rewriting |
| GET | `/stream/{session_id}/segment?u=...` | HLS segment proxy |

### Admin API (`/api/v1/livetv/admin/...`, admin-only)

| Method | Path | Purpose |
|---|---|---|
| GET/POST/PUT/DELETE | `/sources/m3u` (+ `/{id}`) | CRUD |
| GET/POST/PUT/DELETE | `/sources/xmltv` (+ `/{id}`) | CRUD |
| POST | `/sources/m3u/{id}/refresh` | Manual refresh |
| POST | `/sources/xmltv/{id}/refresh` | Manual refresh |
| GET | `/channels` | Admin channel list (overrides, enabled flag) |
| PATCH | `/channels/{id}` | Override channel number, group, position, enabled |
| GET | `/channels/{id}/epg-keys` | Linked XMLTV ids |
| POST | `/channels/{id}/epg-keys` | Add manual link |
| DELETE | `/channels/{id}/epg-keys/{xmltv_channel_id}` | Remove link |
| GET | `/sessions` | Active stream sessions |
| POST | `/sessions/{id}/kill` | Force-close a session |
| GET / PUT | `/settings` | Global defaults |

### Public

None for MVP. (Future "TV" capability for native apps will route through the same `/api/v1/livetv/...` API; no separate public surface is planned.)

---

## 6. Stream proxy mechanics

### Session creation

1. Client calls `POST /api/v1/livetv/channels/{id}/stream`.
2. Plugin enforces: caller can see channel, caller is under `per_user_stream_cap`, channel is under its per-channel cap.
3. Plugin asks the host for a scoped grant via `MintScopedStream` (audience: this plugin; TTL: 10 min, refreshable on subsequent stream calls).
4. Plugin inserts a `stream_sessions` row, returns:
   ```json
   {
     "session_id": "...",
     "playback_url": "/api/v1/livetv/stream/<session_id>.m3u8",
     "expires_at": "..."
   }
   ```
5. The choice between `.m3u8` and `.ts` is driven by `channels.upstream_kind`, populated on first stream by HEAD/sniff and cached on the row. Default `unknown` falls back to URL extension; if still ambiguous, the plugin probes the first 188 bytes of the upstream once and decides (MPEG-TS sync byte vs `#EXTM3U` header).

### Token shape

Scoped grant is sent in either:

- A short-lived `livetv_stream` cookie set by the response, scoped to `/api/v1/livetv/stream/`, `HttpOnly`, `Secure`, `SameSite=Lax`. Used by the browser SPA.
- `Authorization: Bearer <grant>` for native apps.

### Streaming path A — raw MPEG-TS

- `GET /api/v1/livetv/stream/{session_id}.ts` validates the scoped token, opens an upstream `GET` against the channel's `upstream_url` using the source's configured `http_headers` (User-Agent, Referer).
- Body is piped to the response with chunked transfer. No buffering beyond a small TCP pipe — the plugin is a passthrough.
- On client disconnect, the upstream connection is cancelled.
- `last_byte_at` is updated every 5s while bytes are flowing.

### Streaming path B — HLS

- `GET /api/v1/livetv/stream/{session_id}.m3u8` fetches the upstream playlist (master or media). Every segment URI and every nested playlist URI is rewritten to a relative `/api/v1/livetv/stream/{session_id}/segment?u=<signed>` URL.
- `u` is `base64url(json({uri, exp}))` HMAC-keyed by a per-session secret stored in `stream_sessions`. This is stateless rewriting — no per-segment DB row.
- `GET /api/v1/livetv/stream/{session_id}/segment?u=...` validates the HMAC, validates `exp` is within the session's scoped grant lifetime, fetches the upstream segment with the source's headers, and proxies the response with its original `Content-Type`. `Cache-Control: no-store`.
- Playlist response is `application/vnd.apple.mpegurl` with `Cache-Control: no-store` since live HLS playlists rotate.

### Headers

The proxy forwards the source's configured `http_headers` on every upstream request. Hop-by-hop headers are stripped from both directions. Range requests are not supported (live).

### Error handling

- Upstream 401/403 → response 502 with `{ "error": "upstream_unauthorized" }`; surfaced in admin sessions list.
- Upstream 404 / connection refused → 502 `{ "error": "upstream_unreachable" }`; SPA shows "Stream unavailable — see admin."
- Internal failures → 500 with structured JSON.

### Session lifecycle

- A scheduled `reap_idle_sessions` job (every 1 min) closes sessions with `now() - last_byte_at > settings.session_idle_timeout` (default 60s).
- Scoped grant is revoked at session end.
- Admin can force-kill via `POST /api/v1/livetv/admin/sessions/{id}/kill` — closes the proxy goroutine, revokes the grant.

### Concurrency

- `per_user_stream_cap` (default 3) enforced at session creation.
- `per_channel_default_cap` (default 5) enforced at session creation; can be overridden per channel for sources with tight credential limits (e.g., a provider that allows 1 simultaneous stream).
- No fan-out (one viewer = one upstream). MVP limitation; revisit when DVR/recording lands.

---

## 7. Web SPA

Stack mirrors audiobooks: React 19, Vite, Tailwind v4, react-router v7, TanStack Query, radix-ui primitives, lucide icons, sonner toasts. Embedded into the Go binary via `embed.go`.

### User pages

- `Home.tsx` — three rails: recently watched, favorites, "On now" (current programs across favorites).
- `Channels.tsx` — channel cards with logo + current program; group filter chips; search; favorite toggle; click to play.
- `Guide.tsx` — headline view. Two-axis virtualized grid (TanStack Virtual rows + columns) covering `settings.guide_window_cap` (default 24h, scrubbable −1d / +14d). Sticky channels column, sticky time row, color-coded categories. Hover for quick info; click for `ProgramDetail` modal with "Play now" if currently airing.
- `Player.tsx` — `hls.js` for HLS, `mpegts.js` for MPEG-TS in browsers. Shows now/next plus a "what's on this channel" rail.
- `Search.tsx` — EPG search results in the current window.
- `Favorites.tsx` — drag-to-reorder.
- `ProgramDetail.tsx` — modal route; description, credits, schedule, "Play channel now."

### Admin pages (`src/pages/admin/`)

- `Sources.tsx` — M3U + XMLTV source list, add/edit/delete, status badge, "Refresh now," last-error tooltip.
- `Channels.tsx` — admin channel list with enable/disable, channel-number override, group rename, manual EPG-link picker.
- `Settings.tsx` — refresh defaults, guide window cap, concurrency caps.
- `Sessions.tsx` — active stream sessions with kill button.

### Cross-cutting

- `api/` — typed fetch wrappers per resource (matches audiobooks `web/src/api/`).
- `lib/auth.ts` — pulls Continuum session from host as audiobooks does.
- TanStack Query `refetchInterval`: guide queries refetch on the next program-boundary minute; sessions admin refetches every 30s.
- Times stored UTC, rendered in user TZ via `Intl.DateTimeFormat`.

---

## 8. Manifest

```json
{
  "id": "continuum.livetv",
  "name": "Live TV",
  "version": "0.1.0",
  "description": "IPTV / M3U live TV portal with XMLTV EPG.",
  "capabilities": ["http_routes.v1", "scheduled_task.v1", "event_consumer.v1"],
  "config_schema": [
    {
      "key": "database_url",
      "title": "Postgres DSN",
      "required": true,
      "json_schema": "{\"type\":\"string\"}",
      "admin_form": { "control": "ADMIN_FORM_CONTROL_PASSWORD" }
    }
  ]
}
```

All non-DSN settings are in DB and editable via the plugin's admin UI.

---

## 9. Testing strategy

- Go unit tests for `internal/m3u` and `internal/xmltv` parsers with checked-in fixtures: standard M3U, M3U with quirks (no `tvg-id`, exotic group titles, BOM, unicode), XMLTV with credits / categories / icons / sub-titles / multi-day windows, gzipped XMLTV.
- `internal/refresh` tests use httptest servers returning fixtures with ETag/Last-Modified to verify conditional GET and 304 handling.
- `internal/store` tests against testcontainers-go Postgres — channel upsert, soft-disable on missing, EPG window prune, favorite ordering.
- `internal/streamproxy` tests with a fake upstream httptest server serving MPEG-TS bytes and a tiny HLS playlist; assert URL rewriting, header passthrough, scoped-token validation, idle reaping, concurrency caps.
- `internal/server` HTTP handler tests for user and admin APIs, table-driven.
- Playwright e2e in `web/e2e`: admin adds an M3U + XMLTV source, refresh completes, user sees channels, opens guide, clicks current program, player loads, scoped token works.
- `docs/setup-debug-flows.md` with manual verification checklist, matching audiobooks.

---

## 10. Known limitations / future work

- No DVR / recording — separate plugin once watch-only is stable.
- No upstream fan-out — each viewer holds its own upstream connection. Will matter once provider credential limits start biting; a tee/SPSC ring buffer is the obvious next step.
- No FFmpeg / remux. Sources that browsers can't play (e.g., some MPEG-TS variants on Safari) require the native/TV app.
- No native app UI — only API stability. Native Live TV tab is a follow-up project.
- No HDHomeRun or tuner-hardware backend. Future capability plugin if interest exists.
- No catchup/timeshift — `catchup*` EXTINF attrs are preserved in `attrs` jsonb but not surfaced.

---

## 11. Build & verification commands

```bash
go test ./...
cd web && pnpm install && pnpm test && pnpm test:e2e
make build
```

End-to-end smoke (manual):

1. Install plugin, set DSN, start.
2. Add an M3U source (e.g., a known free playlist).
3. Add an XMLTV source.
4. Wait for first refresh (or click "Refresh now").
5. Open the SPA — verify channels appear, guide populates.
6. Click a program currently airing — player loads, network tab shows requests against `/api/v1/livetv/stream/...`, scoped cookie set, bytes flowing.
7. Stop playback, wait 90s, verify session reaped in admin.
