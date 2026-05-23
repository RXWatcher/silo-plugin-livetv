# Operations

Operator runbook for the `silo.livetv` plugin. Distinguish:

- **Customer-facing surfaces** — `/channels`, `/guide`, `/search`, `/favorites`, `/player/:channelId`. These are what end users see in the host UI; an operator's job here is making sure channels populate and play.
- **Admin-facing surfaces** — everything under `/admin/*` in the SPA and `/api/v1/livetv/admin/*` on the API. Sources, channel overrides, session control, runtime settings.

For one-off troubleshooting jump to [debugging](debugging.md). This page is the steady-state runbook.

## First-run checklist

1. Provision the role + schema (`README.md` has the SQL).
2. Install the plugin; supply `database_url` with `search_path=livetv` on the DSN.
3. Wait for the Silo admin "Plugins" view to flip the plugin to running. Confirm with `GET /api/v1/livetv/healthz` → 204.
4. Open `/admin/sources` in the host UI and add at least one M3U source.
5. Refresh it. Expect `last_status='ok'` within 5-30s for typical playlists.
6. Add at least one XMLTV source. Refresh it.
7. Visit `/channels`; pick a channel; confirm playback. Confirm an active row appears in `/admin/sessions`.

## Managing sources

### M3U sources

- **Add**: name, URL, optional `http_headers` JSON. The headers map is sent verbatim on every upstream call for that source — refresh GET, stream proxy, segment proxy, and the upstream-kind probe. Use this for `User-Agent`, `Referer`, or auth headers some providers require.
- **Refresh cadence**: default `0 */6 * * *` is hard-coded in the manifest. The per-source `refresh_interval` column exists in the schema and is editable in the admin UI but does not currently drive the cron; it is honoured by external tooling that calls the refresh endpoint on its own schedule.
- **Manual refresh**: `POST /admin/sources/m3u/{id}/refresh` returns 202 immediately and runs the refresh in a background goroutine with a 5-minute timeout. Watch `last_refreshed_at` / `last_status` to track progress.
- **Removed channels**: any `source_channel_id` absent from the latest fetch is soft-disabled (`enabled_src=false`). If you'd flipped `enabled_admin=true` on it, it stays tunable; otherwise it disappears from `/channels`. Hard-deleting an M3U source cascades to `channels` and `stream_sessions`.

### XMLTV sources

- Same CRUD model as M3U sources. `gzip` is a metadata flag; the parser detects gzip on the wire regardless.
- After each successful refresh, programmes older than `now - 6h` are pruned globally — there is no per-source retention knob.
- The worker calls `ReplaceFutureForChannel` per XMLTV channel id, so a partial fetch (one channel missing) only blanks that channel's future window for the duration until the next successful run. Historical (within the 6h grace) programmes survive.

### Channel overrides

`/admin/channels` exposes the (src, admin, effective) triplet for every channel. Operators can patch:

- `channel_number_admin` — display number override.
- `group_title_admin` — re-group a channel without editing the upstream M3U.
- `enabled_admin` — force on/off independent of `enabled_src`. Setting `true` keeps a channel visible even after the upstream drops it.
- `position` — drag-order in the grid.

All overrides are independent of each other; the PATCH body uses `{"set": true, "value": ...}` per field so partial updates leave untouched fields alone.

EPG link management lives on `/admin/channels/{id}/epg-keys`:
- `GET` lists the xmltv ids linked to a channel (both auto and manual).
- `POST {"xmltv_channel_id": "..."}` adds a manual link (`auto_linked=false`). Manual links survive XMLTV refreshes.
- `DELETE` removes any link. Auto-links removed this way will be re-added on the next XMLTV refresh; pair the delete with a `tvg_id` override on the channel if you want it gone for good.

## Runtime settings

`/admin/settings` writes to the singleton `settings` row and triggers `Snapshot.Reload`, so changes take effect on the very next request without a restart.

| Field | Default | Affects |
| --- | --- | --- |
| `default_m3u_refresh` | `6h` | Reserved for the per-source override path; default cron is in the manifest. |
| `default_xmltv_refresh` | `3h` | Same. |
| `guide_window_cap` | `24h` | Hard cap on `GET /guide` windows. Larger requests are clamped. |
| `per_user_stream_cap` | `3` | Max concurrent sessions per user. 0 disables the cap. |
| `per_channel_default_cap` | `5` | Max concurrent sessions per channel. 0 disables. |
| `session_idle_timeout` | `60s` | How long a session can go without a segment fetch before the reaper ends it. |

Caps return `429 user_cap_exceeded` or `429 channel_cap_exceeded` from session mint when hit; the SPA renders the JSON error body.

## Session admin

`/admin/sessions` lists active rows (rows with `ended_at IS NULL`), ordered by start time. Each row carries:

- session id, user id, channel id
- started/last byte timestamps
- bytes streamed
- client IP and user agent (for incident triage)

`POST /admin/sessions/{id}/kill` sets `ended_at=now`, `end_reason='admin_kill'`. The killed client's next segment request returns 404 `session gone`. The SPA does not auto-remint on a kill — the page must be reloaded.

End reasons you'll see in the table when querying directly:

| `end_reason` | Set by | Meaning |
| --- | --- | --- |
| `client_disconnect` | MPEG-TS pump | Clean EOF, write error, or client cancellation |
| `upstream_error` | MPEG-TS pump | Upstream non-2xx or mid-stream read failure |
| `admin_kill` | Admin kill route | Operator-initiated |
| `idle` | Reaper | `last_byte_at < now - session_idle_timeout` |

(HLS sessions don't write a `client_disconnect` themselves; they end via the reaper when the player goes silent.)

## Backups and migrations

- Run `pg_dump --schema=livetv` to back up. Restore into a fresh schema before flipping the plugin over.
- New migrations live in `internal/migrate/files/`. Numbering is monotonic; the runner records what it has applied and is idempotent.
- The plugin applies migrations on startup. To dry-run, point at a scratch database — the runner will create the same tables and inserts.

## Tests

- `make test-go` runs unit + integration tests. The store tests spin up Postgres via the helper in `internal/testutil/`.
- `make test-web` runs Vitest on the SPA components.
- Playwright e2e lives in `web/e2e/`; `cmd/livetv-e2e-server` is the standalone fixture server it talks to.

## Observability

- `GET /api/v1/livetv/healthz` is the cheap liveness probe (204).
- Plugin logs go through hclog. Named loggers worth grepping:
  - `m3u` — refresh per-source success/failure.
  - `xmltv` — same shape.
  - `reaper` (currently logs reaped session counts at info level).
  - `stream` — upstream fetch errors, sniff fallbacks.
- For session bookkeeping, query `stream_sessions` directly — it's the source of truth.
