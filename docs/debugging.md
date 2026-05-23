# Debugging

Symptom-first guide. For background on how subsystems interact, read [architecture.md](architecture.md).

## Refresh failures

The source row in `/admin/sources` is the source of truth: every refresh attempt updates `last_status`, `last_refreshed_at`, and (on success) `etag` / `last_modified`. Open the row first.

### `last_status='ok'` but channels still don't appear

- Confirm `enabled_src=true` on the channels (in `/admin/channels`). The refresh marks channels missing from the latest fetch as `enabled_src=false`.
- Channels with `enabled_admin=false` are hidden no matter what `enabled_src` says.
- The `channels.source_m3u_id` filter on `/admin/channels?source_m3u_id=<id>` narrows the view; clear it if you're looking at the wrong source.

### `last_status='error: HTTP 401'` or `HTTP 403`

The upstream is gating on credentials. Most providers gate by `User-Agent`; some also want `Referer`. Edit the source row and set `http_headers`, e.g.:

```json
{"User-Agent": "VLC/3.0.18 LibVLC/3.0.18", "Referer": "https://provider.example/"}
```

The headers map is sent verbatim to every upstream call for that source — refresh, stream proxy, and the upstream-kind sniff. There's no separate header config for the stream path.

### `last_status='error: HTTP 5xx'`

Provider-side outage. The plugin will retry on its next cron tick; nothing to do beyond confirming the URL works from the host network (`curl -I` from the plugin's container).

### `last_status='error: dial tcp ...'`

DNS or egress failure from the plugin process. Check the plugin host's outbound network. Common when the host runs in a sandbox that blocks egress to anything except the configured Silo core.

### `last_status='error: m3u: missing #EXTM3U header'`

The upstream returned something other than an M3U — usually an HTML error page, login page, or captcha. Open the URL in a browser to confirm. The parser strips a leading UTF-8 BOM before checking the header, so BOMs are not the cause.

### `last_status='error: m3u: empty input'`

The body was zero bytes. The provider sent a 200 with no payload, or all bytes were filtered out before parsing. Hit the URL with `curl -i` to confirm.

### `last_status='error: xmltv: token: ...'`

The XMLTV document is malformed. `ParseAuto` transparently handles gzip on the wire, so a gzip mismatch is not the cause; the body itself is broken XML. If the document used to parse and the provider didn't change, suspect a truncated download (network hiccup) — the next refresh will recover.

### 304 Not Modified is sticky

After a successful refresh the worker saves the upstream's `ETag` and `Last-Modified` and uses them as `If-None-Match` / `If-Modified-Since` on every subsequent request. If you re-uploaded content with the same hash, the upstream may keep returning 304 and the channel table will not change. The recorded status is `ok` (304 is success). To force a re-parse, edit the source row and re-save — the round-trip currently does not blank ETag, so if you really need to bust it, run a SQL `UPDATE m3u_sources SET etag = '', last_modified = '' WHERE id = '...'` and refresh.

## Stream / playback issues

The session row in `stream_sessions` is the source of truth. Every byte route increments `last_byte_at`/`bytes_streamed`, and the session ends with an `end_reason` you can join on.

### `<video>` is blank, devtools shows 401 on `/stream/...`

The session cookie didn't reach the byte route. Two common causes:

1. **Cookie scope mismatch.** The cookie is set with `Path={basePath}/stream/`. If the SPA and the API are served from different paths (e.g. SPA at `/livetv/` and API at `/api/v1/livetv/`), the browser will not attach the cookie to segment requests. Serve both from the same registered origin.
2. **Origin mismatch.** The cookie is `SameSite=Lax` and `Secure`. Cross-origin requests (e.g. SPA on `app.example.com` calling API on `api.example.com`) won't carry it.

A 401 specifically means the cookie was missing or malformed. A 404 (see below) means the cookie was good but the session is gone.

### `<video>` is blank, devtools shows 404 "session gone"

The session ended after the player loaded the URL. Pull the session from the table:

```sql
SELECT id, user_id, channel_id, end_reason, started_at, ended_at, last_byte_at
FROM stream_sessions WHERE id = '<session id from URL>';
```

- `end_reason='idle'` — the reaper ended it. The default `session_idle_timeout` is 60s, which is aggressive for HLS players that buffer ahead. Raise it in `/admin/settings`.
- `end_reason='admin_kill'` — operator killed it.
- `end_reason='upstream_error'` — upstream broke mid-stream; the player should retry by re-minting via `POST /channels/{id}/stream`.

The SPA does not auto-remint; a page refresh on the channel page will mint a new session.

### Player loads in Safari but breaks in Chrome / Firefox

The HLS player flow uses `hls.js` everywhere except Safari, which has native HLS. The MPEG-TS player flow uses `mpegts.js` everywhere; Safari can't play MPEG-TS at all.

Common causes:

- **MPEG-TS with no PCR / malformed PMT.** `mpegts.js` is stricter than FFmpeg-based decoders. The native TV apps work around this; the browser cannot. Documented limitation; the workaround is a TV app.
- **HLS without `EXT-X-VERSION`.** Some providers serve playlists that `hls.js` rejects but Safari accepts. Inspect the network tab for the `.m3u8` body.
- **CORS on rewritten segments.** The plugin rewrites all segment URIs into same-origin paths, so cross-origin CORS isn't the issue. If you see CORS errors in the console, the SPA is on a different origin than the API — fix the deployment.

### Player works briefly, then stalls

- For HLS: the playlist URL TTL is 5 minutes (the HMAC token's `exp`). Players that don't re-fetch the manifest will start 401-ing on segments after that. `hls.js` does re-fetch by default; check that you haven't disabled the live playlist refresh.
- For MPEG-TS: a stall is almost always the upstream dropping us. The session will flip to `end_reason='upstream_error'` and the SPA will need a reload.

### Per-user or per-channel cap hits

`POST /channels/{id}/stream` returns 429 with body:

```json
{"error": {"code": "user_cap_exceeded", "message": "user stream cap 3 reached"}}
```

or `channel_cap_exceeded`. Active sessions counted are those with `ended_at IS NULL`. The reaper runs every minute, so a session that hit the cap by being orphaned (browser tab closed without disconnect) clears within ~60s of the configured idle timeout.

For an immediate clear: `POST /admin/sessions/{id}/kill` on the offending row.

### High CPU on the plugin process

The stream proxy does not transcode. Sustained CPU on the plugin process is almost always a runaway client reconnect loop. Look in `/admin/sessions` for sessions cycling start/end within seconds, or sessions multiplying for the same `(user_id, channel_id)`.

### `upstream_kind` keeps being `unknown`

The probe (`detectUpstreamKind`) runs on the first stream mint for a channel and persists its answer. If the upstream consistently returns content the probe can't classify, the channel will alternate between probes on every mint and default to `mpegts` after each. Two fixes:

- Set the upstream URL to include `.m3u8` or `.ts` so the URL-shortcut step catches it.
- Manually patch the row: `UPDATE channels SET upstream_kind = 'hls' WHERE id = '...'` (the schema constrains it to `mpegts`/`hls`/`unknown`).

## EPG issues

### Channel exists but the guide row is empty

The channel has no entries in `channel_epg_keys`, or the linked xmltv channel ids have no future programmes.

1. `GET /admin/channels/{id}/epg-keys` to see what's linked. Empty?
   - Confirm the XMLTV source ran successfully (`/admin/sources` xmltv tab).
   - Compare `channels.source_channel_id` with the xmltv document's `<channel id="...">` values. If they don't match, the auto-link inserts nothing and you need a manual link.
2. Auto-link is present but no programmes show?
   - The XMLTV document may not include future programmes for that channel id. The 6h prune means everything `stop_utc < now - 6h` is gone after the next refresh.
   - Confirm a non-empty programme query: `SELECT count(*) FROM programs WHERE xmltv_channel_id = '<id>' AND stop_utc > now()`.

### EPG was working, then disappeared overnight

The provider rotated `tvg-id` between refreshes. The new M3U fetch creates a new `source_channel_id` for the same display name, leaving the old channel with `enabled_src=false` and the new channel with no auto-link until the next XMLTV refresh — which only re-links if the xmltv document also got the new id.

Workaround: add a manual EPG link from the new channel to the original xmltv channel id. Manual links carry `auto_linked=false` and survive future refreshes.

### Guide window won't grow past 24h

`guide_window_cap` is 24h by default. `GET /guide?start=...&end=...` silently clamps `end = start + cap` if the request exceeds it. Raise the cap in `/admin/settings` if you really need longer windows; the practical limit is "how far ahead the XMLTV provider's data goes."

### Search returns nothing

`GET /programs/search?q=...` matches against `title`, `sub_title`, and `description`. Searches against programmes outside the loaded window (older than `now - 6h`) will miss because of the prune. A search for a programme that aired three days ago needs an XMLTV source that still emits historical entries; the plugin does not retain them.

## Misc

### Admin manual refresh returns 202 but nothing happens

The 202 is fire-and-forget. The work runs in a background goroutine with a 5-minute timeout. Watch the source row for `last_refreshed_at` to advance and `last_status` to update; nothing else surfaces the failure.

If the goroutine errors, the failure is logged via the hclog `m3u` or `xmltv` logger. The 5-minute cap means a hung upstream eventually surfaces as `last_status='error: ... context deadline exceeded'`.

### `database_url` configured but everything 500s

Most likely the DSN doesn't include `search_path=livetv`. Migrations succeed against whatever schema is in the search path; without an explicit override, they apply to `public` and the plugin then queries against `livetv` tables that don't exist. Symptom is "relation does not exist" errors at startup or first request.

Verify with a quick `\dt livetv.*` in `psql` after install.

### The reaper is more aggressive than expected

Default `session_idle_timeout` is **60 seconds**. That's short for HLS players that may pause segment fetches during ad insertion or live edge sliding. If users report sessions ending unexpectedly, raise it (`300s` is reasonable for HLS, `30s` is reasonable for raw MPEG-TS).

The reaper runs every minute (`* * * * *`), so the effective time-to-reap is `idle_timeout + up to 60s`.

### Cookie says `Secure` but my dev environment is HTTP

The session cookie is set with `Secure: true` unconditionally (see `internal/streamproxy/session.go`). Browsers won't store secure cookies over plain HTTP, so playback will fail in any non-HTTPS dev environment unless you proxy through a TLS-terminating front-end. There is no toggle for this — production assumes HTTPS.
