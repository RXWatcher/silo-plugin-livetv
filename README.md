# Live TV for Continuum

`continuum.livetv` is Continuum's customer-facing live TV portal. It ingests IPTV M3U playlists and XMLTV electronic program guides, presents a channel grid, EPG, and in-browser player, and proxies stream segments to clients through scoped, idle-reapable sessions.

## Category

Lives under **Video / LiveTV** in the admin sidebar. The plugin registers an HTTP routes capability that hosts both the user portal and the admin UI, plus three scheduled tasks that keep playlists, guide data, and stream sessions healthy.

## Capabilities

| Type | ID | Purpose |
| --- | --- | --- |
| `http_routes.v1` | `portal` | Mounts the Live TV portal, admin UI, and stream proxy under `/api/v1/livetv/*` and serves the embedded SPA. |
| `scheduled_task.v1` | `refresh_m3u_sources` | Re-downloads configured M3U playlists (default `0 */6 * * *`) and refreshes the channel table. |
| `scheduled_task.v1` | `refresh_xmltv_sources` | Re-downloads configured XMLTV EPG feeds (default `0 */3 * * *`) and refreshes program data. |
| `scheduled_task.v1` | `reap_idle_sessions` | Closes live-TV stream sessions whose clients have stopped pulling segments (default `* * * * *`). |

## Dependencies

Standalone. The plugin does not subscribe to other Continuum plugins; it only needs the Continuum host for its user-id header, scheduler, and routing surface. Optional companions in the catalog: [`continuum-plugin-notifications`](https://github.com/RXWatcher/continuum-plugin-notifications) (could surface refresh failures or session events) and [`continuum-plugin-stream-dashboard`](https://github.com/RXWatcher/continuum-plugin-stream-dashboard) (could monitor active livetv sessions).

Host: [`ContinuumApp/continuum`](https://github.com/ContinuumApp/continuum). SDK: [`ContinuumApp/continuum-plugin-sdk`](https://github.com/ContinuumApp/continuum-plugin-sdk).

## External services

- M3U playlist URLs supplied by the operator (one or more IPTV providers).
- XMLTV EPG URLs supplied by the operator (often distinct from the M3U origin).
- Upstream stream URLs referenced by each M3U entry; the plugin proxies HLS (`.m3u8`) and MPEG-TS (`.ts`) bytes through to clients with scoped session cookies, without transcoding.

## Customer-facing features

- Channel grid grouped by M3U `group-title`, with favorites and recently watched lists.
- Virtualized EPG grid with sticky time axis and full-text program search.
- In-browser player using `hls.js` for HLS, `mpegts.js` for MPEG-TS, and native HLS in Safari.
- Per-user favorites (reorderable) and recent-channels tracking.
- Admin UI under `/admin/*` for sources, per-channel `tvg_id` overrides, EPG link keys, live sessions, and runtime settings.

## Configuration

| Key | Required | Description |
| --- | --- | --- |
| `database_url` | yes | Postgres DSN scoped to the `livetv` schema, e.g. `postgres://plugin_livetv:...@host:5432/continuum?search_path=livetv&sslmode=disable`. |

Everything else lives in the admin UI at `/admin/settings` and is editable at runtime without a restart: M3U and XMLTV source URLs, refresh intervals, per-user and global concurrency caps, and idle-session timeouts used by the reaper.

The plugin applies its own migrations at startup. To provision the role and schema:

```sql
CREATE ROLE plugin_livetv WITH LOGIN PASSWORD '<chosen>';
CREATE SCHEMA livetv AUTHORIZATION plugin_livetv;
GRANT CONNECT ON DATABASE continuum TO plugin_livetv;
```

## Detailed docs

- [Setup, debugging, and communication flows](docs/setup-debug-flows.md)
- [Design spec](docs/spec/2026-05-21-livetv-plugin-design.md)
- [Implementation plan](docs/plan/2026-05-21-livetv-plugin.md)

## Build and release

CI builds linux-amd64 binaries on push to main via the reusable workflow in [RXWatcher/continuum-plugin-repository](https://github.com/RXWatcher/continuum-plugin-repository) and publishes them to the catalog at [`./binaries/`](https://github.com/RXWatcher/continuum-plugin-repository/tree/main/binaries).
