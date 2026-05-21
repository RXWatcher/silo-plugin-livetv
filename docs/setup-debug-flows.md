# Live TV Portal Setup, Debugging, And Flows

Plugin ID: `continuum.livetv`
Version documented: `0.1.0`

## Purpose

User-facing IPTV / M3U live TV portal with XMLTV electronic program guide,
favorites, recents, EPG search, an admin surface for sources and per-channel
overrides, and a stream proxy that mints scoped session grants for the
embedded player.

## Prerequisites

- Continuum plugin host with a working installation flow.
- Postgres 14+ with permission to create a role and dedicated schema.
- An M3U playlist URL from your IPTV provider (or a local file served over
  HTTP). Optional but recommended: an XMLTV EPG URL covering the same
  channel ids as the M3U.

## Database Setup

Create a dedicated role and schema before installing the plugin:

```sql
CREATE ROLE plugin_livetv WITH LOGIN PASSWORD '<chosen>';
CREATE SCHEMA livetv AUTHORIZATION plugin_livetv;
GRANT CONNECT ON DATABASE continuum TO plugin_livetv;
```

The plugin applies its own migrations at startup; no manual table creation
is required. The DSN used at install time must include
`search_path=livetv`.

## Configuration Reference

- `database_url` (required, secret) - Postgres DSN scoped to the `livetv`
  schema.

Everything else (refresh intervals, concurrency caps, idle timeouts) lives
in `/admin/settings` and is editable at runtime without restarting the
plugin.

## First-run Operator Flow

1. Install `continuum.livetv` from the host and supply `database_url` in
   the install form.
2. Open the Live TV portal in the host UI. With no sources configured the
   landing page directs operators to `/admin/sources`.
3. On `/admin/sources`, click "Add M3U source" and supply:
   - **Name** - human label.
   - **URL** - the playlist URL.
   - Optional **HTTP headers** - JSON object, e.g. `{"User-Agent": "..."}`
     for providers that gate on UA.
4. Click "Refresh" on the new row. Wait for `last_status` to flip to `ok`.
   This typically lands within 5-30 s depending on playlist size.
5. Switch to the XMLTV tab, click "Add XMLTV source", supply URL, save,
   refresh. Wait for `last_status='ok'`.
6. On `/admin/channels`, optionally set per-channel `tvg_id` overrides or
   add extra EPG link keys for channels whose M3U `tvg-id` differs from
   the XMLTV `channel id`.
7. Visit `/channels` to confirm channels populated. Tune to a channel to
   verify playback.

## Exposed Routes

User API (mounted under `/api/v1/livetv`, authenticated):

- `GET /channels`
- `GET /channels/{id}`
- `GET /groups`
- `GET /guide`
- `GET /programs/{id}`
- `GET /programs/search`
- `GET /favorites`
- `POST /favorites/{channel_id}`
- `DELETE /favorites/{channel_id}`
- `POST /favorites/reorder`
- `GET /recent`
- `POST /channels/{id}/stream`

Stream byte routes (cookie-scoped session, no session header):

- `GET /stream/{session_id}.ts`
- `GET /stream/{session_id}.m3u8`
- `GET /stream/{session_id}/segment`

Admin API (admin-only):

- `GET POST /admin/sources/m3u`
- `GET PUT DELETE POST(/refresh) /admin/sources/m3u/{id}`
- `GET POST /admin/sources/xmltv`
- `GET PUT DELETE POST(/refresh) /admin/sources/xmltv/{id}`
- `GET PATCH /admin/channels` and `/admin/channels/{id}`
- `GET POST /admin/channels/{id}/epg-keys`
- `DELETE /admin/channels/{id}/epg-keys/{xmltv_channel_id}`
- `GET /admin/sessions` and `POST /admin/sessions/{id}/kill`
- `GET PUT /admin/settings`

Probe:

- `GET /healthz` returns 204 (no auth).

## Capabilities

- `http_routes.v1 (portal)` - the SPA, user API, admin API, and stream
  proxy bytes.
- `scheduled_task.v1 (refresh_m3u_sources)` - default cron `0 */6 * * *`.
- `scheduled_task.v1 (refresh_xmltv_sources)` - default cron `0 */3 * * *`.
- `scheduled_task.v1 (reap_idle_sessions)` - cron `* * * * *`.

## Refresh Debugging

The first place to look is the source row in `/admin/sources`:

- `last_status='ok'` means the parser succeeded and rows were upserted.
- `last_status='error: <text>'` records the failure verbatim.

Common error patterns:

- `HTTP 401` / `HTTP 403` - upstream is gating on credentials. Most often
  fixed by setting a `User-Agent` (and sometimes `Referer`) on the source's
  `http_headers` JSON.
- `dial tcp ...` - DNS or egress failure from the plugin host network.
- `missing #EXTM3U` (or `missing <tv>` for XMLTV) - the upstream returned
  HTML, captcha, or an error page. Open the URL in a browser to confirm.
- `parse: ...` - the playlist or EPG is malformed. The parsers are
  tolerant of unknown attributes but require the canonical envelope.

Manual re-run from a shell:

```bash
curl -X POST -H "X-Continuum-Admin: true" -H "X-Continuum-User-Id: <op>" \
  https://<host>/api/v1/livetv/admin/sources/m3u/<id>/refresh
```

Scheduled-task runs log per source via `hclog` at info level; failures
log at error level with the wrapped error.

## Stream Debugging

The stream session lifecycle is the source of truth:

- `stream_sessions` table records every session, the channel, the user,
  the start, the last activity, and (when the session ends) the
  `end_reason`.
- `end_reason='idle_reaped'` - reaper closed the session because no
  segment was fetched within the configured idle timeout.
- `end_reason='admin_killed'` - operator clicked "kill" in
  `/admin/sessions`.
- `end_reason='upstream_error'` - the proxy could not reach the upstream
  (logged with the underlying error).
- `end_reason='cap_evicted'` - replaced because the user or global cap
  was reached.

Symptoms and fixes:

- **`<video>` is blank, devtools shows 401 on `/stream/...`** - the
  session cookie scope does not match the player origin. The cookie is
  scoped to the plugin's base path (`/api/v1/livetv`) so the host must
  proxy the SPA and the API at the same origin.
- **`<video>` is blank, devtools shows 404 "session gone"** - the
  session ended (idle reaper or admin kill). The SPA auto-mints a fresh
  one on retry; if it does not, refresh the page.
- **Playback works in Safari but not Chrome/Edge/Firefox** - Safari has
  native HLS; the other browsers go through `hls.js` (HLS) or
  `mpegts.js` (MPEG-TS). Some MPEG-TS variants (no PCR, malformed PMT)
  will not demux in the browser; native TV apps with FFmpeg are the
  workaround.
- **High CPU on the plugin process** - the stream proxy is pass-through
  and does no transcoding; high CPU usually means a runaway client
  reconnect loop. Inspect `/admin/sessions` for rapidly-cycling rows.

## Concurrency And Caps

Global caps are editable in `/admin/settings`:

- Maximum concurrent sessions per user.
- Maximum concurrent sessions globally.
- Idle timeout (seconds) after which the reaper ends a session.

The settings snapshot is shared with the stream proxy and the idle
reaper, so edits take effect on the next request / next reaper tick
without a restart.

Per-channel concurrency caps are tracked as a known limitation; the
MVP enforces only per-user and global caps.

## How This Plugin Communicates

- Pulls M3U playlists and XMLTV documents from operator-configured
  upstream URLs via `http.DefaultClient`.
- Proxies stream bytes (MPEG-TS and HLS) from the same upstream to the
  authenticated session client.
- Reads `X-Continuum-User-Id` and `X-Continuum-Admin` headers off the
  host-proxied request to authenticate API calls.

The plugin does not call other plugins.

## Log And Health Checks

- Confirm the plugin process is up via Continuum Admin -> Plugins.
- `GET /api/v1/livetv/healthz` returns 204 when the chi router is alive.
- Plugin startup logs cover manifest load, migrations, settings
  snapshot, scheduler registration, and gRPC serve.
- Scheduled-task logs are emitted by the `m3u`, `xmltv`, and `reaper`
  named loggers; grep on those to find a specific subsystem.

## Common Failure Patterns

- `database_url` points at the public schema instead of `livetv`;
  migrations land in the wrong place and lookups return empty.
- Reverse proxy forwards `/` (the SPA) but not `/api/v1/livetv/*` or the
  `/stream/...` byte routes; player gets 404s on segment fetches.
- Upstream provider rotates `tvg-id` between refreshes; channels appear
  to lose their EPG. Add per-channel `tvg_id` overrides or extra EPG
  link keys in `/admin/channels`.
- Player loads fine in dev (same origin) but breaks in prod (different
  origin behind a CDN). The stream session cookie is `SameSite=Lax`; the
  SPA must be served from the same registered origin as the API.

## Verification After Changes

1. Reload the plugin installation.
2. Open `/admin/sources` and confirm at least one M3U source shows
   `last_status='ok'` with a recent `last_run_at`.
3. Trigger a manual refresh on each source type; confirm rows update.
4. Tune to a channel; confirm a row appears in `/admin/sessions` and
   advances `last_active_at`.
5. Wait for the idle reaper tick; confirm idle sessions end with
   `idle_reaped`.

## Known Limitations

- No per-channel concurrency cap (only per-user and global).
- No transcoding; client must demux the upstream stream natively.
- No automatic XMLTV-to-channel name fuzzy match; mismatches require
  manual `tvg_id` overrides in `/admin/channels`.
- No multi-tenant isolation beyond the host's user-id header; a single
  installation serves a single tenant's channel list.
