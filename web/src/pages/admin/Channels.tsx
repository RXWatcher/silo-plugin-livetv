import { useEffect, useMemo, useState } from 'react';
import { useSearchParams } from 'react-router';
import { Dialog } from 'radix-ui';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Plus, Tv, X as XIcon } from 'lucide-react';
import { toast } from 'sonner';
import {
  adminApi,
  type AdminChannel,
  type AdminChannelPatch,
} from '@/api/admin';
import { cn } from '@/lib/utils';
import { ToggleSwitch, Field } from './Sources';

// Channels admin page: a table of every channel imported from any M3U source,
// with admin-side overrides for number/group/enabled/position and a chip
// editor for the XMLTV channel id keys that wire programs to this channel.
//
// The PATCH endpoint uses a three-valued envelope:
//   omitted              → leave column untouched
//   {set:true, value:X}  → set X
//   {set:true, value:null} → clear override (drop back to upstream value)
// The drawer encodes that semantically: each override row has a toggle for
// "use override?" plus an input for the value. Saving emits {set:true,
// value:...} for toggled-on rows, {set:true, value:null} for rows the user
// turned off after we initially loaded an existing override, and absent
// otherwise.
export function Channels() {
  const [searchParams, setSearchParams] = useSearchParams();
  const sourceFilter = searchParams.get('source_m3u_id') ?? '';
  const [editing, setEditing] = useState<AdminChannel | null>(null);

  const sourcesQuery = useQuery({
    queryKey: ['admin', 'sources', 'm3u'],
    queryFn: () => adminApi.sources.m3uList(),
  });
  const channelsQuery = useQuery({
    queryKey: ['admin', 'channels', { source_m3u_id: sourceFilter || undefined }],
    queryFn: () =>
      adminApi.channels.list(
        sourceFilter ? { source_m3u_id: sourceFilter } : undefined,
      ),
  });

  const sourceNameById = useMemo(() => {
    const m = new Map<string, string>();
    for (const s of sourcesQuery.data?.data ?? []) m.set(s.id, s.name);
    return m;
  }, [sourcesQuery.data]);

  const channels = channelsQuery.data?.data ?? [];

  return (
    <div className="space-y-4">
      <header className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <h1 className="text-lg font-semibold tracking-tight">Channels</h1>
          <p className="text-xs text-[color:var(--color-muted-foreground)]">
            Imported channels and per-channel overrides. Effective values are what users see.
          </p>
        </div>
        <select
          value={sourceFilter}
          onChange={(e) =>
            setSearchParams(
              (sp) => {
                const next = new URLSearchParams(sp);
                if (e.target.value) next.set('source_m3u_id', e.target.value);
                else next.delete('source_m3u_id');
                return next;
              },
              { replace: true },
            )
          }
          className="form-input sm:w-72"
        >
          <option value="">All sources</option>
          {(sourcesQuery.data?.data ?? []).map((s) => (
            <option key={s.id} value={s.id}>
              {s.name}
            </option>
          ))}
        </select>
      </header>

      {channelsQuery.isPending ? (
        <div className="py-10 text-center text-sm text-[color:var(--color-muted-foreground)]">Loading channels…</div>
      ) : channelsQuery.isError ? (
        <div className="rounded-md border border-red-900/40 bg-red-950/30 p-4 text-sm text-red-300">
          Could not load channels: {(channelsQuery.error as Error).message}
        </div>
      ) : channels.length === 0 ? (
        <div className="rounded-lg border border-dashed border-[color:var(--color-border)] p-10 text-center text-sm text-[color:var(--color-muted-foreground)]">
          No channels yet. Add an M3U source and trigger a refresh.
        </div>
      ) : (
        <div className="overflow-auto rounded-lg border border-[color:var(--color-border)]">
          <table className="w-full border-collapse text-sm">
            <thead className="bg-[color:var(--color-surface)] text-[11px] uppercase tracking-wider text-[color:var(--color-muted-foreground)]">
              <tr>
                <th className="px-3 py-2 text-left font-semibold">Logo</th>
                <th className="px-3 py-2 text-left font-semibold">Name</th>
                <th className="px-3 py-2 text-left font-semibold">Source</th>
                <th className="px-3 py-2 text-left font-semibold">Number</th>
                <th className="px-3 py-2 text-left font-semibold">Group</th>
                <th className="px-3 py-2 text-left font-semibold">Position</th>
                <th className="px-3 py-2 text-left font-semibold">Enabled</th>
                <th className="px-3 py-2 text-left font-semibold">EPG keys</th>
                <th className="px-3 py-2 text-right font-semibold">Actions</th>
              </tr>
            </thead>
            <tbody>
              {channels.map((c) => (
                <ChannelRow
                  key={c.id}
                  channel={c}
                  sourceName={sourceNameById.get(c.source_m3u_id) ?? c.source_m3u_id}
                  onEdit={() => setEditing(c)}
                />
              ))}
            </tbody>
          </table>
        </div>
      )}

      {editing ? (
        <EditChannelDrawer
          channel={editing}
          onClose={() => setEditing(null)}
        />
      ) : null}
    </div>
  );
}

function ChannelRow({
  channel,
  sourceName,
  onEdit,
}: {
  channel: AdminChannel;
  sourceName: string;
  onEdit: () => void;
}) {
  return (
    <tr className="border-t border-[color:var(--color-border)] align-top">
      <td className="px-3 py-2">
        <div className="flex h-8 w-8 items-center justify-center overflow-hidden rounded bg-black/40">
          {channel.logo_url ? (
            <img src={channel.logo_url} alt="" className="max-h-full max-w-full object-contain" loading="lazy" />
          ) : (
            <Tv size={14} className="text-[color:var(--color-muted-foreground)]" />
          )}
        </div>
      </td>
      <td className="px-3 py-2 font-medium">{channel.display_name}</td>
      <td className="px-3 py-2 text-xs text-[color:var(--color-muted-foreground)]">{sourceName}</td>
      <td className="px-3 py-2 text-xs">
        <OverrideTriplet
          src={channel.channel_number_src}
          admin={channel.channel_number_admin ?? null}
          effective={channel.channel_number_effective}
        />
      </td>
      <td className="px-3 py-2 text-xs">
        <OverrideTriplet
          src={channel.group_title_src}
          admin={channel.group_title_admin ?? null}
          effective={channel.group_title_effective}
        />
      </td>
      <td className="px-3 py-2 font-mono text-xs tabular-nums">{channel.position}</td>
      <td className="px-3 py-2">
        <span
          className={cn(
            'rounded-full px-2 py-0.5 text-[11px]',
            channel.enabled_effective
              ? 'bg-emerald-500/15 text-emerald-300'
              : 'bg-zinc-700/40 text-[color:var(--color-muted-foreground)]',
          )}
        >
          {channel.enabled_effective ? 'on' : 'off'}
        </span>
        {channel.enabled_admin != null ? (
          <span className="ml-1 text-[10px] text-[color:var(--color-muted-foreground)]">
            (override)
          </span>
        ) : null}
      </td>
      <td className="px-3 py-2">
        <EPGKeyChips channelId={channel.id} />
      </td>
      <td className="px-3 py-2 text-right">
        <button
          type="button"
          onClick={onEdit}
          className="rounded-md border border-[color:var(--color-border)] px-2.5 py-1 text-xs hover:bg-[color:var(--color-surface)]"
        >
          Edit
        </button>
      </td>
    </tr>
  );
}

function OverrideTriplet({
  src,
  admin,
  effective,
}: {
  src: string;
  admin: string | null;
  effective: string;
}) {
  const hasOverride = admin != null;
  return (
    <div className="space-y-0.5">
      <div className="font-medium text-[color:var(--color-foreground)]">{effective || '—'}</div>
      {hasOverride ? (
        <div className="text-[10px] text-[color:var(--color-muted-foreground)]">
          src: {src || '—'} · override: {admin}
        </div>
      ) : src ? (
        <div className="text-[10px] text-[color:var(--color-muted-foreground)]">src: {src}</div>
      ) : null}
    </div>
  );
}

// ───────────────────────────── EPG key chips ─────────────────────────────

function EPGKeyChips({ channelId }: { channelId: string }) {
  const qc = useQueryClient();
  const [adding, setAdding] = useState(false);
  const [draft, setDraft] = useState('');

  const queryKey = ['admin', 'channels', channelId, 'epg-keys'];
  const keysQuery = useQuery({
    queryKey,
    queryFn: () => adminApi.channels.epgKeys(channelId),
  });

  const addMutation = useMutation({
    mutationFn: (key: string) => adminApi.channels.addEPGKey(channelId, key),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey });
      setDraft('');
      setAdding(false);
      toast.success('EPG key added');
    },
    onError: (err: Error) => toast.error(`Add EPG key failed: ${err.message}`),
  });
  const removeMutation = useMutation({
    mutationFn: (key: string) => adminApi.channels.removeEPGKey(channelId, key),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey });
      toast.success('EPG key removed');
    },
    onError: (err: Error) => toast.error(`Remove EPG key failed: ${err.message}`),
  });

  const keys = keysQuery.data?.xmltv_channel_ids ?? [];

  return (
    <div className="flex flex-wrap items-center gap-1">
      {keysQuery.isPending && keys.length === 0 ? (
        <span className="text-[11px] text-[color:var(--color-muted-foreground)]">…</span>
      ) : null}
      {keys.map((k) => (
        <span
          key={k}
          className="inline-flex items-center gap-1 rounded-full border border-[color:var(--color-border)] bg-[color:var(--color-surface)] px-2 py-0.5 text-[11px]"
        >
          <span className="font-mono">{k}</span>
          <button
            type="button"
            title="Remove"
            onClick={() => removeMutation.mutate(k)}
            className="text-[color:var(--color-muted-foreground)] hover:text-red-400"
          >
            <XIcon size={10} />
          </button>
        </span>
      ))}
      {adding ? (
        <form
          onSubmit={(e) => {
            e.preventDefault();
            const v = draft.trim();
            if (!v) return;
            addMutation.mutate(v);
          }}
          className="inline-flex items-center gap-1"
        >
          <input
            autoFocus
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            onBlur={() => {
              if (!draft.trim()) setAdding(false);
            }}
            placeholder="xmltv_channel_id"
            className="form-input py-0.5 text-[11px] font-mono w-44"
          />
          <button
            type="submit"
            className="rounded bg-amber-500 px-1.5 py-0.5 text-[11px] font-semibold text-black hover:bg-amber-400"
          >
            Add
          </button>
        </form>
      ) : (
        <button
          type="button"
          onClick={() => setAdding(true)}
          className="inline-flex items-center gap-0.5 rounded-full border border-dashed border-[color:var(--color-border)] px-2 py-0.5 text-[11px] text-[color:var(--color-muted-foreground)] hover:bg-[color:var(--color-surface)]"
        >
          <Plus size={10} /> Add
        </button>
      )}
    </div>
  );
}

// ───────────────────────────── Edit drawer ─────────────────────────────

interface OverrideRowState<T> {
  // The original value that was on the row when we loaded it. If it's not
  // null and the user toggles `useOverride` off, we need to emit a clear
  // ({set:true, value:null}) on save. If it was null, we just omit the key.
  original: T | null;
  useOverride: boolean;
  value: T;
}

function EditChannelDrawer({
  channel,
  onClose,
}: {
  channel: AdminChannel;
  onClose: () => void;
}) {
  const qc = useQueryClient();

  const [num, setNum] = useState<OverrideRowState<string>>({
    original: channel.channel_number_admin ?? null,
    useOverride: channel.channel_number_admin != null,
    value: channel.channel_number_admin ?? channel.channel_number_src ?? '',
  });
  const [group, setGroup] = useState<OverrideRowState<string>>({
    original: channel.group_title_admin ?? null,
    useOverride: channel.group_title_admin != null,
    value: channel.group_title_admin ?? channel.group_title_src ?? '',
  });
  const [enabled, setEnabled] = useState<OverrideRowState<boolean>>({
    original: channel.enabled_admin ?? null,
    useOverride: channel.enabled_admin != null,
    value: channel.enabled_admin ?? channel.enabled_src,
  });
  const [position, setPosition] = useState<OverrideRowState<number>>({
    // Position is a single column on the channel — no src/admin/effective
    // split — but we expose it through the patch envelope for parity.
    original: channel.position,
    useOverride: true,
    value: channel.position,
  });

  // Track whether the user actually touched any field so we don't fire a
  // patch that re-clears every column on save.
  const [touched, setTouched] = useState<{
    num: boolean;
    group: boolean;
    enabled: boolean;
    position: boolean;
  }>({ num: false, group: false, enabled: false, position: false });

  useEffect(() => {
    // If the channel updates externally while the drawer is open (e.g. a
    // refresh fires), close to avoid stale state.
  }, [channel.id]);

  const saveMutation = useMutation({
    mutationFn: () => {
      const patch: AdminChannelPatch = {};
      // String overrides: only emit when user touched the row (so opening
      // and saving without changes is a no-op).
      if (touched.num) {
        patch.channel_number_admin = num.useOverride
          ? { set: true, value: num.value.trim() || null }
          : { set: true, value: null };
      }
      if (touched.group) {
        patch.group_title_admin = group.useOverride
          ? { set: true, value: group.value.trim() || null }
          : { set: true, value: null };
      }
      if (touched.enabled) {
        patch.enabled_admin = enabled.useOverride
          ? { set: true, value: enabled.value }
          : { set: true, value: null };
      }
      if (touched.position) {
        patch.position = { set: true, value: position.value };
      }
      return adminApi.channels.patch(channel.id, patch);
    },
    onMutate: async () => {
      await qc.cancelQueries({ queryKey: ['admin', 'channels'] });
      const previous = qc.getQueriesData<{ data: AdminChannel[] }>({
        queryKey: ['admin', 'channels'],
      });
      // Optimistically rewrite the rows so the table reflects the new
      // override values immediately. Effective values are recomputed:
      // override wins, else fall back to src.
      qc.getQueriesData<{ data: AdminChannel[] }>({ queryKey: ['admin', 'channels'] }).forEach(
        ([key, data]) => {
          if (!data) return;
          qc.setQueryData(key as readonly unknown[], {
            ...data,
            data: data.data.map((c) =>
              c.id === channel.id
                ? applyLocalPatch(c, {
                    num: touched.num
                      ? num.useOverride
                        ? num.value.trim()
                        : null
                      : undefined,
                    group: touched.group
                      ? group.useOverride
                        ? group.value.trim()
                        : null
                      : undefined,
                    enabled: touched.enabled
                      ? enabled.useOverride
                        ? enabled.value
                        : null
                      : undefined,
                    position: touched.position ? position.value : undefined,
                  })
                : c,
            ),
          });
        },
      );
      return { previous };
    },
    onError: (err: Error, _vars, ctx) => {
      ctx?.previous.forEach(([key, data]) => qc.setQueryData(key as readonly unknown[], data));
      toast.error(`Save failed: ${err.message}`);
    },
    onSuccess: () => {
      toast.success('Channel updated');
      qc.invalidateQueries({ queryKey: ['admin', 'channels'] });
      onClose();
    },
  });

  return (
    <Dialog.Root open onOpenChange={(o) => { if (!o) onClose(); }}>
      <Dialog.Portal>
        <Dialog.Overlay className="fixed inset-0 z-40 bg-black/60 backdrop-blur-sm" />
        <Dialog.Content className="fixed right-0 top-0 z-50 flex h-dvh w-full max-w-md flex-col overflow-y-auto border-l border-[color:var(--color-border)] bg-[color:var(--color-background)] shadow-xl">
          <header className="flex items-center justify-between border-b border-[color:var(--color-border)] px-5 py-3">
            <div>
              <Dialog.Title className="text-sm font-semibold">{channel.display_name}</Dialog.Title>
              <p className="text-[11px] text-[color:var(--color-muted-foreground)]">{channel.id}</p>
            </div>
            <Dialog.Close
              className="rounded p-1 text-[color:var(--color-muted-foreground)] hover:bg-[color:var(--color-surface)]"
              aria-label="Close"
            >
              <XIcon size={16} />
            </Dialog.Close>
          </header>
          <form
            className="flex flex-1 flex-col gap-4 px-5 py-4"
            onSubmit={(e) => {
              e.preventDefault();
              saveMutation.mutate();
            }}
          >
            <OverrideStringRow
              label="Channel number"
              srcValue={channel.channel_number_src}
              state={num}
              setState={(next) => {
                setNum(next);
                setTouched((t) => ({ ...t, num: true }));
              }}
            />
            <OverrideStringRow
              label="Group"
              srcValue={channel.group_title_src}
              state={group}
              setState={(next) => {
                setGroup(next);
                setTouched((t) => ({ ...t, group: true }));
              }}
            />
            <OverrideBoolRow
              label="Enabled"
              srcValue={channel.enabled_src}
              state={enabled}
              setState={(next) => {
                setEnabled(next);
                setTouched((t) => ({ ...t, enabled: true }));
              }}
            />
            <Field label="Position" hint="Sort key used by the user channel list.">
              <input
                type="number"
                value={position.value}
                onChange={(e) => {
                  setPosition({ ...position, value: Number(e.target.value) || 0 });
                  setTouched((t) => ({ ...t, position: true }));
                }}
                className="form-input"
              />
            </Field>

            <div className="mt-auto flex items-center justify-end gap-2 border-t border-[color:var(--color-border)] pt-3">
              <button
                type="button"
                onClick={onClose}
                className="rounded-md border border-[color:var(--color-border)] px-3 py-1.5 text-sm hover:bg-[color:var(--color-surface)]"
              >
                Cancel
              </button>
              <button
                type="submit"
                disabled={saveMutation.isPending}
                className="rounded-md bg-amber-500 px-3 py-1.5 text-sm font-semibold text-black hover:bg-amber-400 disabled:opacity-60"
              >
                {saveMutation.isPending ? 'Saving…' : 'Save overrides'}
              </button>
            </div>
          </form>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}

function OverrideStringRow({
  label,
  srcValue,
  state,
  setState,
}: {
  label: string;
  srcValue: string;
  state: OverrideRowState<string>;
  setState: (next: OverrideRowState<string>) => void;
}) {
  return (
    <div className="space-y-1.5">
      <div className="flex items-center justify-between">
        <span className="text-sm">{label}</span>
        <div className="flex items-center gap-2 text-[11px] text-[color:var(--color-muted-foreground)]">
          <span>Use override?</span>
          <ToggleSwitch
            checked={state.useOverride}
            onChange={(v) => setState({ ...state, useOverride: v })}
          />
        </div>
      </div>
      <input
        value={state.useOverride ? state.value : srcValue}
        disabled={!state.useOverride}
        onChange={(e) => setState({ ...state, value: e.target.value })}
        placeholder={srcValue || '—'}
        className="form-input w-full"
      />
      <div className="text-[10px] text-[color:var(--color-muted-foreground)]">
        Source value: {srcValue || '—'}
      </div>
    </div>
  );
}

function OverrideBoolRow({
  label,
  srcValue,
  state,
  setState,
}: {
  label: string;
  srcValue: boolean;
  state: OverrideRowState<boolean>;
  setState: (next: OverrideRowState<boolean>) => void;
}) {
  return (
    <div className="space-y-1.5 rounded-md border border-[color:var(--color-border)] bg-[color:var(--color-surface)] p-3">
      <div className="flex items-center justify-between">
        <span className="text-sm">{label}</span>
        <div className="flex items-center gap-2 text-[11px] text-[color:var(--color-muted-foreground)]">
          <span>Use override?</span>
          <ToggleSwitch
            checked={state.useOverride}
            onChange={(v) => setState({ ...state, useOverride: v })}
          />
        </div>
      </div>
      <div className="flex items-center justify-between text-sm">
        <span className="text-[color:var(--color-muted-foreground)]">
          Value: {state.useOverride ? (state.value ? 'on' : 'off') : srcValue ? 'on (src)' : 'off (src)'}
        </span>
        <ToggleSwitch
          checked={state.useOverride ? state.value : srcValue}
          onChange={(v) => setState({ ...state, value: v })}
        />
      </div>
    </div>
  );
}

// applyLocalPatch is the optimistic mirror of the server's effective-value
// computation: override wins if set, otherwise the upstream src value takes
// over. Used by the patch mutation's onMutate to keep the table in sync.
function applyLocalPatch(
  c: AdminChannel,
  patch: {
    num?: string | null;
    group?: string | null;
    enabled?: boolean | null;
    position?: number;
  },
): AdminChannel {
  const next = { ...c };
  if (patch.num !== undefined) {
    next.channel_number_admin = patch.num;
    next.channel_number_effective = patch.num != null ? patch.num : c.channel_number_src;
  }
  if (patch.group !== undefined) {
    next.group_title_admin = patch.group;
    next.group_title_effective = patch.group != null ? patch.group : c.group_title_src;
  }
  if (patch.enabled !== undefined) {
    next.enabled_admin = patch.enabled;
    next.enabled_effective = patch.enabled != null ? patch.enabled : c.enabled_src;
  }
  if (patch.position !== undefined) {
    next.position = patch.position;
  }
  return next;
}
