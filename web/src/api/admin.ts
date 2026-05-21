// Admin-side API wrappers for the Live TV plugin. Phase 9 calls these from
// the lazy-loaded /admin/* SPA routes; nothing in the user portal touches
// them, which keeps the user bundle slim.
//
// Conventions mirror api/client.ts: shared transport via fetch with
// credentials: 'include', list responses wrapped in { data, next_cursor? },
// errors thrown as { status, details? } via the same surface as the user
// client.

import { mountPath } from '@/lib/mountPath';
import type { ApiError, ListEnvelope } from '@/api/client';

function apiBase(): string {
  return `${mountPath()}/api/v1/livetv`;
}

// req mirrors the user-side req<T> but lives here to keep the admin module
// fully self-contained — the user bundle never imports admin.ts so duplicating
// 30 lines is the right tradeoff for the lazy-load split.
async function req<T>(path: string, init?: RequestInit): Promise<T> {
  const headers: Record<string, string> = {
    Accept: 'application/json',
    ...(init?.headers as Record<string, string> | undefined),
  };
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
      // body wasn't JSON; fall through with the status-line message.
    }
    const err = new Error(message) as ApiError;
    err.status = res.status;
    if (details) err.details = details;
    throw err;
  }
  if (res.status === 204 || res.status === 205 || res.headers.get('Content-Length') === '0') {
    return undefined as T;
  }
  const text = await res.text();
  if (text.length === 0) return undefined as T;
  return JSON.parse(text) as T;
}

// ───────────────────────────── Source DTOs ─────────────────────────────

export interface AdminM3USource {
  id: string;
  name: string;
  url: string;
  http_headers: Record<string, string>;
  enabled: boolean;
  refresh_interval: string; // Go duration string ("6h", "6h0m0s")
  last_refreshed_at?: string;
  last_status?: string;
  etag?: string;
  last_modified?: string;
}

export interface AdminXMLTVSource extends AdminM3USource {
  gzip: boolean;
}

export interface AdminM3USourceInput {
  name: string;
  url: string;
  http_headers: Record<string, string>;
  enabled: boolean;
  refresh_interval: string;
}

export interface AdminXMLTVSourceInput extends AdminM3USourceInput {
  gzip: boolean;
}

// ───────────────────────────── Channel DTOs ─────────────────────────────

export interface AdminChannel {
  id: string;
  source_m3u_id: string;
  source_channel_id: string;
  display_name: string;
  channel_number_src: string;
  channel_number_admin?: string | null;
  channel_number_effective: string;
  logo_url: string;
  group_title_src: string;
  group_title_admin?: string | null;
  group_title_effective: string;
  upstream_url: string;
  upstream_kind: string;
  enabled_src: boolean;
  enabled_admin?: boolean | null;
  enabled_effective: boolean;
  position: number;
}

// PatchField is the three-valued envelope used by every patch entry. Omit
// the field entirely to leave the column untouched; pass {set:true,
// value:null} to clear an override; pass {set:true, value:X} to set.
export interface PatchField<T> {
  set: boolean;
  value: T | null;
}

export interface AdminChannelPatch {
  channel_number_admin?: PatchField<string>;
  group_title_admin?: PatchField<string>;
  enabled_admin?: PatchField<boolean>;
  position?: PatchField<number>;
}

export interface EPGKeysEnvelope {
  xmltv_channel_ids: string[];
}

// ───────────────────────────── Session DTOs ─────────────────────────────

export interface AdminSession {
  id: string;
  user_id: string;
  channel_id: string;
  started_at: string;
  last_byte_at: string;
  bytes_streamed: number;
  client_ip?: string;
  user_agent?: string;
}

// ───────────────────────────── Settings DTO ─────────────────────────────

export interface AdminSettings {
  default_m3u_refresh: string;
  default_xmltv_refresh: string;
  guide_window_cap: string;
  per_user_stream_cap: number;
  per_channel_default_cap: number;
  session_idle_timeout: string;
}

// ───────────────────────────── adminApi surface ─────────────────────────

export const adminApi = {
  sources: {
    m3uList: () => req<ListEnvelope<AdminM3USource>>('/admin/sources/m3u'),
    m3uCreate: (input: AdminM3USourceInput) =>
      req<AdminM3USource>('/admin/sources/m3u', {
        method: 'POST',
        body: JSON.stringify(input),
      }),
    m3uGet: (id: string) =>
      req<AdminM3USource>(`/admin/sources/m3u/${encodeURIComponent(id)}`),
    m3uUpdate: (id: string, input: AdminM3USourceInput) =>
      req<AdminM3USource>(`/admin/sources/m3u/${encodeURIComponent(id)}`, {
        method: 'PUT',
        body: JSON.stringify(input),
      }),
    m3uDelete: (id: string) =>
      req<void>(`/admin/sources/m3u/${encodeURIComponent(id)}`, { method: 'DELETE' }),
    m3uRefresh: (id: string) =>
      req<{ started: boolean }>(`/admin/sources/m3u/${encodeURIComponent(id)}/refresh`, {
        method: 'POST',
      }),

    xmltvList: () => req<ListEnvelope<AdminXMLTVSource>>('/admin/sources/xmltv'),
    xmltvCreate: (input: AdminXMLTVSourceInput) =>
      req<AdminXMLTVSource>('/admin/sources/xmltv', {
        method: 'POST',
        body: JSON.stringify(input),
      }),
    xmltvGet: (id: string) =>
      req<AdminXMLTVSource>(`/admin/sources/xmltv/${encodeURIComponent(id)}`),
    xmltvUpdate: (id: string, input: AdminXMLTVSourceInput) =>
      req<AdminXMLTVSource>(`/admin/sources/xmltv/${encodeURIComponent(id)}`, {
        method: 'PUT',
        body: JSON.stringify(input),
      }),
    xmltvDelete: (id: string) =>
      req<void>(`/admin/sources/xmltv/${encodeURIComponent(id)}`, { method: 'DELETE' }),
    xmltvRefresh: (id: string) =>
      req<{ started: boolean }>(`/admin/sources/xmltv/${encodeURIComponent(id)}/refresh`, {
        method: 'POST',
      }),
  },

  channels: {
    list: (params?: { source_m3u_id?: string }) => {
      const q = new URLSearchParams();
      if (params?.source_m3u_id) q.set('source_m3u_id', params.source_m3u_id);
      const suffix = q.toString();
      return req<ListEnvelope<AdminChannel>>(
        `/admin/channels${suffix ? '?' + suffix : ''}`,
      );
    },
    patch: (id: string, patch: AdminChannelPatch) =>
      req<AdminChannel>(`/admin/channels/${encodeURIComponent(id)}`, {
        method: 'PATCH',
        body: JSON.stringify(patch),
      }),
    epgKeys: (id: string) =>
      req<EPGKeysEnvelope>(`/admin/channels/${encodeURIComponent(id)}/epg-keys`),
    addEPGKey: (id: string, xmltvChannelId: string) =>
      req<void>(`/admin/channels/${encodeURIComponent(id)}/epg-keys`, {
        method: 'POST',
        body: JSON.stringify({ xmltv_channel_id: xmltvChannelId }),
      }),
    removeEPGKey: (id: string, xmltvChannelId: string) =>
      req<void>(
        `/admin/channels/${encodeURIComponent(id)}/epg-keys/${encodeURIComponent(xmltvChannelId)}`,
        { method: 'DELETE' },
      ),
  },

  sessions: {
    list: () => req<ListEnvelope<AdminSession>>('/admin/sessions'),
    kill: (id: string) =>
      req<void>(`/admin/sessions/${encodeURIComponent(id)}/kill`, { method: 'POST' }),
  },

  settings: {
    get: () => req<AdminSettings>('/admin/settings'),
    put: (values: AdminSettings) =>
      req<AdminSettings>('/admin/settings', {
        method: 'PUT',
        body: JSON.stringify(values),
      }),
  },
};

// Exposed for tests / parity with client.ts.
export const _adminInternals = { apiBase };
