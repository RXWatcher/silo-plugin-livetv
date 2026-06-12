# Architecture

Internal layout of `silo.livetv`. Read the [README](../README.md) first for the capability list and external contract; this page is the inside view.

## Process model

The binary is a single Go process started by the Silo host. On boot it:

1. Loads the embedded manifest and answers the host's GetManifest RPC.
2. Receives `database_url` via Configure and opens a pgx pool against the `livetv` schema.
3. Runs the embedded SQL migrations under `internal/migrate/files/` against that schema, in numeric order. Migrations are idempotent and safe to run on every start.
4. Loads the singleton `settings` row into an in-memory `settings.Snapshot` so the stream proxy, guide handler, and idle reaper can read it without a DB round-trip per request.
5. Registers four capability servers over gRPC:
   - `http_routes.v1` (the chi router built in `internal/server`).
   - Three `scheduled_task.v1` workers dispatched by capability id from `internal/scheduler`.

No background goroutines run independently of the host. M3U/XMLTV refreshes and the reaper are dispatched on cron ticks from the host scheduler; manual refresh from the admin UI spawns a one-off goroutine with a 5-minute timeout (see `adminRefreshTimeout` in `internal/server/admin_sources.go`).

## Package map

```
cmd/
  silo-plugin-livetv/   main, manifest, capability wiring
  livetv-e2e-server/         standalone server for Playwright e2e
internal/
  m3u/        playlist parser (#EXTM3U / #EXTINF)
  xmltv/      EPG parser, transparent gzip via ParseAuto
  migrate/    embedded .sql files + runner
  store/      pgx-backed CRUD per table
  refresh/    M3UWorker, XMLTVWorker, ReapIdle
  scheduler/  scheduled_task.v1 gRPC bridge → workers/reaper
  server/     chi router, user API, admin API, middleware
  streamproxy/ session minting, HLS rewrite, MPEG-TS pump, segment HMAC
  settings/   in-memory Snapshot of the singleton row
  runtime/    Runtime construction, capability fan-out
  httproutes/ http_routes.v1 capability server wrapper
  testutil/   pgx test pool + fixtures
web/          React 19 SPA, embedded via embed.go
```

## Schema overview

All tables live in the `livetv` Postgres schema. Owned by the `plugin_livetv` role.

| Table | Purpose | Notable columns |
| --- | --- | --- |
| `m3u_sources` | M3U source rows | `http_headers` (jsonb), `etag`, `last_modified`, `last_status` |
| `xmltv_sources` | XMLTV source rows | same shape + `gzip` flag (informational; parser auto-detects) |
| `settings` | Singleton runtime knobs (`id=1` check) | refresh intervals, guide window cap, concurrency caps, idle timeout |
| `channels` | Effective channel list | `(source_m3u_id, source_channel_id)` unique; `*_admin` override columns; `upstream_kind` ∈ {mpegts, hls, unknown} |
| `channel_epg_keys` | M:N channel ↔ xmltv channel id | `auto_linked=true` for the M3U-tvg-id→xmltv-id match; `false` for manual links |
| `programs` | EPG entries keyed by `xmltv_channel_id` | indexed on `(xmltv_channel_id, start_utc)` and `(start_utc, stop_utc)` |
| `program_credits` | Cast/crew rows referenced by program id | constrained kind list |
| `user_favorites` | Reorderable per-user channel list | `(user_id, channel_id)` pk, position |
| `user_recent` | Last-tuned timestamps per user/channel | sorted DESC by `last_tuned_at` |
| `stream_sessions` | Live session log (active + archived) | `session_secret` bytea, partial indexes on `ended_at IS NULL` |

Migrations live in `internal/migrate/files/`. Add new ones with the next number; the runner is in `internal/migrate/runner.go`.

## Settings snapshot

`internal/settings/Snapshot` caches the singleton `settings` row in memory under an RW mutex. Reads happen on every stream session mint, every guide request, and every reaper tick — caching keeps these off the DB hot path.

The cache is updated:
- On startup (`settings.Load`).
- After `PUT /admin/settings`, the handler calls `Snapshot.Reload` before returning, so the next stream/guide/reap observes the new values.

The snapshot also satisfies `streamproxy.Settings`, so the stream proxy and the guide handler share one source of truth. `StaticSettings` in `streamproxy` is the test-only variant.

## M3U refresh flow

`refresh.M3UWorker.RefreshAll` walks every enabled `m3u_sources` row and calls `RefreshOne` per source. Per-source errors are logged but do not abort the loop; errors are joined and returned to the scheduler.

`RefreshOne` per source:

1. Build an HTTP GET with the source's `http_headers` map applied verbatim.
2. Apply conditional-GET headers: `If-None-Match` (saved ETag) and `If-Modified-Since` (saved Last-Modified). 304 short-circuits to status `ok` with no DB churn.
3. Parse the body via `m3u.Parse` (lenient on attribute order, requires the `#EXTM3U` envelope, strips UTF-8 BOM).
4. For each entry, derive `source_channel_id`:
   - If `tvg-id` is present, use it verbatim.
   - Else `name:<slug(title)>:<sha8(url)>` so the id is stable across refreshes even when the upstream omits `tvg-id`.
5. Upsert into `channels` keyed on `(source_m3u_id, source_channel_id)`. Existing rows keep their `*_admin` overrides.
6. `Store.MarkChannelsMissing` soft-disables channels (`enabled_src=false`) that didn't appear in the latest fetch. Admin overrides (`enabled_admin=true`) still keep them tunable until cleared.
7. Persist new ETag/Last-Modified and `last_status='ok'`.

On any error after step 1, the source row is updated with `last_status='error: <text>'` and the saved conditional-GET tokens are preserved so the next refresh can still revalidate.

Default cron: `0 */6 * * *` (every six hours). Override per-source via `refresh_interval` in the admin UI (currently informational — the host scheduler drives the cadence).

## XMLTV refresh flow

`refresh.XMLTVWorker` mirrors the M3U worker shape. Per-source steps:

1. HTTP GET with conditional headers, same as M3U.
2. `xmltv.ParseAuto` peeks the first two bytes; if it matches gzip magic (`1f 8b`) the body is decompressed transparently. The `gzip` column on `xmltv_sources` is informational only.
3. Streaming parse: `onChannel` collects channel ids, `onProgramme` buffers programmes per channel.
4. For each accumulated channel: `Store.ReplaceFutureForChannel` atomically swaps future programmes (everything with `start_utc >= now`).
5. `autoLinkEPG` (inlined in `refresh/xmltv.go`) does `INSERT INTO channel_epg_keys (channel_id, xmltv_channel_id, auto_linked=true) SELECT ... FROM channels WHERE source_channel_id = $1 ON CONFLICT DO NOTHING`. Manual links (`auto_linked=false`) are untouched by `ON CONFLICT DO NOTHING`.
6. `Store.PruneOldPrograms` deletes any programme whose `stop_utc < now - 6h` (the `pruneAge` constant in `refresh/xmltv.go`). The 6-hour grace keeps "earlier today" lookups working.

Default cron: `0 */3 * * *`.

## Channel ↔ EPG linking

The fundamental mismatch: M3U entries carry `tvg-id`, XMLTV documents carry `channel id`. They usually agree, but providers rotate ids and operators mix sources, so the schema models the relationship explicitly via `channel_epg_keys`.

- **Auto-link** (`auto_linked=true`): inserted by the XMLTV worker whenever `channels.source_channel_id == xmltv_channel.id`. Re-running XMLTV is safe — duplicates are filtered by `ON CONFLICT DO NOTHING`.
- **Manual link** (`auto_linked=false`): added via `POST /admin/channels/{id}/epg-keys`. Manual links survive XMLTV refreshes for the same reason.
- A channel can have multiple xmltv channel ids; the guide query unions programmes from all of them. Useful for SD/HD pairs that share programming.

The XMLTV worker never deletes rows from `channel_epg_keys`. Operator-initiated `DELETE /admin/channels/{id}/epg-keys/{xmltv_channel_id}` is the only way to break a link.

## Stream proxy

Annotated entry points live in `internal/streamproxy/proxy.go`. The model:

- **Session mint** (`POST /channels/{id}/stream`):
  - Enforces per-user and per-channel concurrency caps from the settings snapshot.
  - Resolves `upstream_kind` (`mpegts` | `hls`). Unknown values trigger `detectUpstreamKind`: URL extension → HEAD probe of `Content-Type` → GET sniff of the first 512 bytes (MPEG-TS sync byte `0x47` or `#EXTM3U`). Result is persisted via `Store.SetUpstreamKind`.
  - Generates a 32-byte random `session_secret`, inserts a `stream_sessions` row with a fresh ULID, and sets a cookie at `Path={basePath}/stream/`, `HttpOnly`, `Secure`, `SameSite=Lax`, value `<session_id>.<hex(secret)>`, TTL 8h.
  - Returns `{session_id, playback_url, expires_at}`. The SPA hands `playback_url` directly to `hls.js` / `mpegts.js` / native HLS.

- **Auth** (`verifyToken`): every byte route extracts the token from cookie or `Authorization: Bearer`. Cookie wins on conflict. Validates the secret with `crypto/subtle.ConstantTimeCompare`. Ended sessions return 404 `session gone`; bad tokens return 401.

- **MPEG-TS pump** (`/stream/{session_id}.ts`): opens the upstream URL with the source's `http_headers`, streams body-for-body in 64 KiB chunks with explicit `Flush()` after each write, debounces DB updates of `last_byte_at` / `bytes_streamed` to every 5 s. Clean EOF or client disconnect ends the session with `end_reason='client_disconnect'`; mid-stream upstream error ends with `'upstream_error'`. No transcoding.

- **HLS playlist** (`/stream/{session_id}.m3u8`): fetches the upstream playlist, rewrites every URI line into `{basePath}/stream/{session_id}/segment?u=<token>`. Token is base64.RawURL(JSON{uri, exp}).base64.RawURL(HMAC-SHA256(payload, session_secret)). Relative URIs are resolved against the upstream URL so segment tokens always carry absolute targets. TTL is 5 minutes (long enough for live edge to slide, short enough that a stolen token expires soon).

- **HLS segment** (`/stream/{session_id}/segment`): verifies the segment token's HMAC against the session secret and the embedded expiry, fetches the upstream segment, copies the body through. Does NOT end the session on completion (segments are short-lived; the reaper handles silent sessions).

## Idle reaper

`refresh.ReapIdle` ends every active session whose `last_byte_at` predates `now - idle_timeout`. The cutoff is read from the snapshot at dispatch time so admin edits to `session_idle_timeout` propagate on the next minute tick.

End reason for reaped rows is `'idle'` (not `'idle_reaped'` — the early plan called it that but the implementation lives in `Store.ReapIdle` in `internal/store/sessions.go` and writes the shorter string).

Default cron: `* * * * *` (every minute). Default idle timeout from migration 0001 is 60 seconds, which is aggressive — bump it in `/admin/settings` if you see legitimate pauses between segment fetches.

## Auth model summary

- The host validates user identity upstream and adds `X-Silo-User-Id` to every request.
- `RequireSession` reflects that header into the request context (no token verification; the host owns that). Missing → 401.
- `RequireAdmin` additionally requires `X-Silo-User-Role: admin` (the host stamps the requesting account's role on every proxied request). Missing/non-admin → 403.
- Stream byte routes bypass both because they authenticate on the cookie/bearer set at session mint. This is why the SPA, the user API, and the stream proxy must all be served from the same registered origin: the cookie's `Path` is scoped to the plugin's base path.

## Frontend

React 19 SPA built with Vite, embedded into the Go binary via `web/embed.go`. Routing splits into `/channels`, `/guide`, `/search`, `/favorites`, `/player/:channelId`, plus `/admin/*` for the operator UI. `mountPath.ts` strips the runtime base path so the SPA can be served from any prefix.

The player page picks `HlsPlayer` or `MpegtsPlayer` based on the suffix of `playback_url` (`.m3u8` vs `.ts`), not on `upstream_kind`, so a future proxy that repackages between formats Just Works. Each player tears down its underlying library on unmount — without `hls.destroy()` / `mpegts.destroy()`, the library keeps pulling segments after the user navigates away.
