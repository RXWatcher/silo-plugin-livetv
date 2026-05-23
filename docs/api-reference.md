# API reference

Everything the plugin serves over HTTP, organised by audience. All routes live under `{base}/api/v1/livetv` except `{base}/healthz`. The Silo host proxies to this prefix; the plugin computes it from the host-supplied base path at boot.

Routes are defined in `internal/server/router.go`. Authentication happens in `internal/server/middleware.go` and `internal/streamproxy/proxy.go`.

## Auth surfaces

| Surface | How | Header / cookie |
| --- | --- | --- |
| Public | None | `GET /healthz` only |
| User | `RequireSession` | `X-Silo-User-Id` (the host sets this on the proxied request) |
| Admin | `RequireAdmin` | `X-Silo-User-Id` + `X-Silo-Admin: true` |
| Stream byte routes | Token verification | Cookie `livetv_stream=<sessionID>.<hex(secret)>` (or `Authorization: Bearer ...`) |

The host owns user identity. The plugin trusts the headers and reflects the user id into request context via `streamproxy.WithUserID`. Don't expose the plugin's port directly to clients — the auth model relies on the host doing the front-line check.

## Public

| Method | Path | Body | Returns |
| --- | --- | --- | --- |
| GET | `/healthz` | — | 204 No Content |

## User API

All require `X-Silo-User-Id`.

### Channels and groups

| Method | Path | Notes |
| --- | --- | --- |
| GET | `/channels` | All visible channels for the user (favorites bit, current/next program populated). |
| GET | `/channels/{id}` | Single channel with the same shape. 404 if not visible. |
| GET | `/groups` | Distinct effective `group_title` values. |

### Guide

| Method | Path | Notes |
| --- | --- | --- |
| GET | `/guide` | Query params: `start`, `end` (RFC3339, default now → +4h), `channels` (repeated), `group`. Window is hard-clamped to `guide_window_cap` (24h default). |
| GET | `/programs/search` | `q=` full-text against title/subtitle/description. |
| GET | `/programs/{id}` | Program detail including credits and rating. |

### Favorites

| Method | Path | Body | Notes |
| --- | --- | --- | --- |
| GET | `/favorites` | — | User's favorites, ordered by position. |
| POST | `/favorites/{channel_id}` | — | Idempotent. |
| DELETE | `/favorites/{channel_id}` | — | Idempotent. |
| POST | `/favorites/reorder` | `{"channel_ids": ["a","b","c"]}` | Replaces position numbers in array order. |

### Recently watched

| Method | Path | Notes |
| --- | --- | --- |
| GET | `/recent` | Last-tuned per channel for the user, DESC by `last_tuned_at`. Best-effort updated on every successful session mint. |

### Stream lifecycle

| Method | Path | Body | Returns |
| --- | --- | --- | --- |
| POST | `/channels/{id}/stream` | — | `{session_id, playback_url, expires_at}` and `Set-Cookie: livetv_stream=...`. 429 on cap. |

Failure JSON shape (all stream errors use this):

```json
{"error": {"code": "user_cap_exceeded", "message": "user stream cap 3 reached"}}
```

Codes: `unauthenticated`, `channel_not_found`, `user_cap_exceeded`, `channel_cap_exceeded`, `internal_error`.

## Stream byte routes

Authenticate on the `livetv_stream` cookie (or `Authorization: Bearer <token>`). The cookie is `Path={base}/stream/`, `HttpOnly`, `Secure`, `SameSite=Lax`, 8h TTL.

| Method | Path | Notes |
| --- | --- | --- |
| GET | `/stream/{session_id}.ts` | Raw MPEG-TS pass-through. Streams until client or upstream closes. Ends session with `client_disconnect` or `upstream_error`. |
| GET | `/stream/{session_id}.m3u8` | Rewritten HLS playlist. Every URI replaced with a signed proxy URL. |
| GET | `/stream/{session_id}/segment?u=<token>` | HLS segment proxy. Token is HMAC-SHA256 of `{uri, exp}` keyed by the session secret. 5 min TTL. |

Common error responses:
- `401 unauthorized` — missing/malformed cookie, or session id in URL doesn't match cookie.
- `404 session gone` — `ended_at` is set (admin kill, idle reaper, or upstream error).
- `502 bad upstream` — upstream HTTP error or transport failure.

## Admin API

All require `X-Silo-User-Id` + `X-Silo-Admin: true`.

### Sources

| Method | Path | Body | Notes |
| --- | --- | --- | --- |
| GET | `/admin/sources/m3u` | — | List with status metadata. |
| POST | `/admin/sources/m3u` | `{name,url,http_headers,enabled,refresh_interval}` | `refresh_interval` is a Go duration string (e.g. `"6h"`). |
| GET | `/admin/sources/m3u/{id}` | — | Single source. |
| PUT | `/admin/sources/m3u/{id}` | Same as POST | |
| DELETE | `/admin/sources/m3u/{id}` | — | Cascades to channels and sessions. |
| POST | `/admin/sources/m3u/{id}/refresh` | — | 202 + background goroutine, 5-min timeout. |

XMLTV routes mirror this exactly under `/admin/sources/xmltv`. Request body adds `gzip` (informational).

### Channels

| Method | Path | Body | Notes |
| --- | --- | --- | --- |
| GET | `/admin/channels?source_m3u_id=...` | — | Admin view with `(src, admin, effective)` triplets for each overridable field. |
| PATCH | `/admin/channels/{id}` | See below | Three-valued patch. |
| GET | `/admin/channels/{id}/epg-keys` | — | `{"xmltv_channel_ids": [...]}` |
| POST | `/admin/channels/{id}/epg-keys` | `{"xmltv_channel_id": "..."}` | Manual link (auto_linked=false). |
| DELETE | `/admin/channels/{id}/epg-keys/{xmltv_channel_id}` | — | Removes any link (auto or manual). |

Patch body uses `{"set": bool, "value": ...}` per field:

```json
{
  "channel_number_admin": {"set": true, "value": "101"},
  "group_title_admin":    {"set": true, "value": null},
  "enabled_admin":        {"set": true, "value": true},
  "position":             {"set": false}
}
```

`set=false` (or the key being absent) leaves the column untouched. `set=true` with `value=null` clears the override.

### Sessions

| Method | Path | Body | Notes |
| --- | --- | --- | --- |
| GET | `/admin/sessions` | — | Active rows (`ended_at IS NULL`), ordered by start. |
| POST | `/admin/sessions/{id}/kill` | — | Idempotent. Sets `end_reason='admin_kill'`. Returns 204 even on unknown id. |

### Settings

| Method | Path | Body | Notes |
| --- | --- | --- | --- |
| GET | `/admin/settings` | — | Singleton row. Durations are Go strings. |
| PUT | `/admin/settings` | Same shape | Validates, persists, reloads the in-memory snapshot before returning. |

All durations must parse with `time.ParseDuration` and be positive. Caps must be > 0.

## Error envelope

The stream proxy uses `{"error": {"code", "message"}}` (see above). The rest of the routes use a simpler shape — `writeError(w, status, err)` / `writeErrorMsg(w, status, msg)` in `internal/server/dto.go` emit:

```json
{"error": "human readable message"}
```

Don't pattern-match on message strings; they're for humans. Use the HTTP status code for branching.

## Caveats

- Routes under `/api/v1/livetv` are mounted by the host proxy. If you proxy `/` but not `/api/v1/livetv/*`, the SPA loads but every fetch 404s.
- The stream byte routes specifically need the cookie scope to match. If the SPA is at one path and the API at another, the cookie won't attach to segment requests. See [debugging.md](debugging.md#video-is-blank-devtools-shows-401-on-stream).
- Long-poll / SSE is not used. The SPA polls `/guide` and `/channels` on user action. The player communicates with the upstream directly through the byte routes.
