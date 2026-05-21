// Typed fetch wrapper for the Live TV plugin API.
//
// Every endpoint matches one of /api/v1/livetv/... — see
// internal/server/router.go for the authoritative route table.
//
// Conventions:
//   - List endpoints return { data: T[], next_cursor?: string }
//   - Errors return { error: string, details?: string } with non-2xx status
//   - All requests carry credentials: 'include' so the host's session cookie
//     and (for stream routes) the per-session cookie set by POST
//     /channels/{id}/stream are forwarded.
//   - The session-creation POST is authenticated by the Continuum host's
//     session, not the per-stream cookie — so we don't need anything special
//     beyond credentials: 'include' there either.

import { mountPath } from '@/lib/mountPath';

function apiBase(): string {
  return `${mountPath()}/api/v1/livetv`;
}

export interface Program {
  id: string;
  channel_id?: string;
  xmltv_channel_id?: string;
  title: string;
  sub_title?: string;
  description?: string;
  start: string;
  stop: string;
  categories?: string[];
  episode_num?: string;
  season_num?: number;
  episode?: number;
  rating?: string;
  icon_url?: string;
  original_air_date?: string;
  credits?: Array<{ kind: string; name: string; position: number }>;
}

export interface ProgramRef {
  id: string;
  title: string;
  start: string;
  stop: string;
}

export interface Channel {
  id: string;
  display_name: string;
  channel_number?: string;
  group_title?: string;
  logo_url?: string;
  upstream_kind?: 'mpegts' | 'hls' | 'unknown' | string;
  is_favorite: boolean;
  current_program?: ProgramRef;
  next_program?: ProgramRef;
}

export interface Favorite {
  channel_id: string;
  position: number;
}

export interface Recent {
  channel_id: string;
  last_tuned_at: string;
}

export interface StartStream {
  session_id: string;
  playback_url: string;
  expires_at: string;
}

export interface ListEnvelope<T> {
  data: T[];
  next_cursor?: string;
}

export interface GuideEnvelope {
  data: Record<string, Program[]>;
  window: { start: string; end: string };
}

export interface ApiError extends Error {
  status: number;
  details?: string;
}

// req is the single source of truth for transport: prepends the plugin mount
// path, sets credentials, parses JSON, and unwraps {error, details} into a
// throwable with status + details preserved on the Error instance.
async function req<T>(path: string, init?: RequestInit): Promise<T> {
  const headers: Record<string, string> = {
    Accept: 'application/json',
    ...(init?.headers as Record<string, string> | undefined),
  };
  // Only set Content-Type when there's a body — GET requests don't need it
  // and adding it triggers a preflight on some proxies.
  if (init?.body != null && !headers['Content-Type']) {
    headers['Content-Type'] = 'application/json';
  }

  const res = await fetch(apiBase() + path, {
    credentials: 'include',
    ...init,
    headers,
  });

  if (!res.ok) {
    let message = `${res.status} ${res.statusText}`;
    let details: string | undefined;
    try {
      const body = await res.json();
      if (body?.error) message = String(body.error);
      if (body?.details) details = String(body.details);
    } catch {
      // body wasn't JSON; fall through with status-line message.
    }
    const err = new Error(message) as ApiError;
    err.status = res.status;
    if (details) err.details = details;
    throw err;
  }

  // 204 No Content / 205 Reset Content / DELETE-with-no-body responses.
  if (res.status === 204 || res.status === 205 || res.headers.get('Content-Length') === '0') {
    return undefined as T;
  }
  // Some handlers (favorites add/remove, reorder) return the standard
  // empty 204 path above; others return an empty body with 200. Guard
  // against an empty body so JSON.parse doesn't throw.
  const text = await res.text();
  if (text.length === 0) return undefined as T;
  return JSON.parse(text) as T;
}

// buildQuery encodes a flat record of search params, skipping undefined/empty
// values. Array values become repeated keys (?channels=a&channels=b), which
// is what the guide handler expects.
function buildQuery(path: string, params?: Record<string, string | number | string[] | undefined>): string {
  if (!params) return path;
  const q = new URLSearchParams();
  for (const [key, val] of Object.entries(params)) {
    if (val == null) continue;
    if (Array.isArray(val)) {
      for (const v of val) {
        if (v != null && v !== '') q.append(key, String(v));
      }
      continue;
    }
    if (val === '' && typeof val === 'string') continue;
    q.set(key, String(val));
  }
  const enc = q.toString();
  return enc ? `${path}?${enc}` : path;
}

export interface ChannelListParams {
  group?: string;
  q?: string;
  cursor?: string;
  limit?: number;
}

export interface GuideParams {
  group?: string;
  channels?: string[];
  q?: string;
}

export const api = {
  // ───────────── Channels & groups ─────────────
  channels: (p?: ChannelListParams) =>
    req<ListEnvelope<Channel>>(buildQuery('/channels', p as Record<string, string | number | undefined>)),

  channel: (id: string) =>
    req<Channel>(`/channels/${encodeURIComponent(id)}`),

  groups: () => req<ListEnvelope<string>>('/groups'),

  // ───────────── EPG: guide window, program detail, search ─────────────
  guide: (start: string, end: string, opts?: GuideParams) =>
    req<GuideEnvelope>(
      buildQuery('/guide', {
        start,
        end,
        group: opts?.group,
        q: opts?.q,
        channels: opts?.channels,
      }),
    ),

  program: (id: string) => req<Program>(`/programs/${encodeURIComponent(id)}`),

  search: (q: string, from?: string, to?: string, limit?: number) =>
    req<ListEnvelope<Program>>(
      buildQuery('/programs/search', { q, from, to, limit }),
    ),

  // ───────────── User state ─────────────
  favorites: () => req<ListEnvelope<Favorite>>('/favorites'),
  addFav: (id: string) =>
    req<void>(`/favorites/${encodeURIComponent(id)}`, { method: 'POST' }),
  delFav: (id: string) =>
    req<void>(`/favorites/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  reorderFav: (ids: string[]) =>
    req<void>('/favorites/reorder', {
      method: 'POST',
      body: JSON.stringify({ channel_ids: ids }),
    }),

  recent: () => req<ListEnvelope<Recent>>('/recent'),

  // ───────────── Playback ─────────────
  // POST /channels/{id}/stream — mints a session; the response carries the
  // playback URL. The session cookie set on this response is scoped to
  // /api/v1/livetv/stream/... and is consumed by hls.js / mpegts.js
  // implicitly via credentials: 'include' in fetch (and via the browser
  // for <video src=...> media requests since the cookie's domain matches).
  startStream: (channelId: string) =>
    req<StartStream>(`/channels/${encodeURIComponent(channelId)}/stream`, {
      method: 'POST',
    }),
};

// Exposed for tests that need to reach into apiBase resolution.
export const _internals = { apiBase };
