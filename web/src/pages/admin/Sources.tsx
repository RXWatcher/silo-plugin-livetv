import { useEffect, useMemo, useState } from 'react';
import { Dialog, Tabs, Switch } from 'radix-ui';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  Pencil,
  Plus,
  RefreshCw,
  Trash2,
  XIcon,
} from 'lucide-react';
import { toast } from 'sonner';
import {
  adminApi,
  type AdminM3USource,
  type AdminM3USourceInput,
  type AdminXMLTVSource,
  type AdminXMLTVSourceInput,
} from '@/api/admin';
import { cn, formatRelative, isLikelyDuration } from '@/lib/utils';

// Sources is the entry point for the /admin/sources route. It hosts two
// near-identical tabs (M3U + XMLTV) and a shared drawer for the edit/create
// form. XMLTV adds the Gzip flag; everything else is the same shape.
export function Sources() {
  return (
    <div className="space-y-4">
      <header className="flex items-center justify-between">
        <h1 className="text-lg font-semibold tracking-tight">Sources</h1>
        <p className="text-xs text-[color:var(--color-muted-foreground)]">
          Upstream M3U + XMLTV feeds that populate channels and the guide.
        </p>
      </header>
      <Tabs.Root defaultValue="m3u" className="space-y-3">
        <Tabs.List className="inline-flex gap-1 rounded-md border border-[color:var(--color-border)] bg-[color:var(--color-surface)] p-1">
          <TabTrigger value="m3u">M3U</TabTrigger>
          <TabTrigger value="xmltv">XMLTV</TabTrigger>
        </Tabs.List>
        <Tabs.Content value="m3u">
          <M3USources />
        </Tabs.Content>
        <Tabs.Content value="xmltv">
          <XMLTVSources />
        </Tabs.Content>
      </Tabs.Root>
    </div>
  );
}

function TabTrigger({ value, children }: { value: string; children: React.ReactNode }) {
  return (
    <Tabs.Trigger
      value={value}
      className="rounded px-3 py-1.5 text-sm font-medium text-[color:var(--color-muted-foreground)] data-[state=active]:bg-[color:var(--color-surface-hover)] data-[state=active]:text-[color:var(--color-foreground)]"
    >
      {children}
    </Tabs.Trigger>
  );
}

// ───────────────────────────── M3U tab ─────────────────────────────

function M3USources() {
  const qc = useQueryClient();
  const [editing, setEditing] = useState<AdminM3USource | null>(null);
  const [creating, setCreating] = useState(false);

  const listQuery = useQuery({
    queryKey: ['admin', 'sources', 'm3u'],
    queryFn: () => adminApi.sources.m3uList(),
  });
  const sources = listQuery.data?.data ?? [];

  const deleteMutation = useMutation({
    mutationFn: (id: string) => adminApi.sources.m3uDelete(id),
    onSuccess: () => {
      toast.success('Source deleted');
      qc.invalidateQueries({ queryKey: ['admin', 'sources', 'm3u'] });
    },
    onError: (err: Error) => toast.error(`Delete failed: ${err.message}`),
  });

  const refreshMutation = useMutation({
    mutationFn: (id: string) => adminApi.sources.m3uRefresh(id),
    onSuccess: () => {
      toast.success('Refresh started');
      // The refresh runs async on the server, so we won't see fresh status
      // for a few seconds. A 5s debounced invalidation hides the lag.
      setTimeout(
        () => qc.invalidateQueries({ queryKey: ['admin', 'sources', 'm3u'] }),
        5_000,
      );
    },
    onError: (err: Error) => toast.error(`Refresh failed: ${err.message}`),
  });

  return (
    <SourcesPanel
      title="M3U playlists"
      sources={sources}
      isLoading={listQuery.isPending}
      error={listQuery.error as Error | null}
      onAdd={() => setCreating(true)}
      onEdit={setEditing}
      onDelete={(id) => deleteMutation.mutate(id)}
      onRefresh={(id) => refreshMutation.mutate(id)}
      renderExtraColumn={null}
    >
      {editing ? (
        <SourceDrawer
          kind="m3u"
          source={editing}
          onClose={() => setEditing(null)}
        />
      ) : null}
      {creating ? (
        <SourceDrawer
          kind="m3u"
          source={null}
          onClose={() => setCreating(false)}
        />
      ) : null}
    </SourcesPanel>
  );
}

// ───────────────────────────── XMLTV tab ─────────────────────────────

function XMLTVSources() {
  const qc = useQueryClient();
  const [editing, setEditing] = useState<AdminXMLTVSource | null>(null);
  const [creating, setCreating] = useState(false);

  const listQuery = useQuery({
    queryKey: ['admin', 'sources', 'xmltv'],
    queryFn: () => adminApi.sources.xmltvList(),
  });
  const sources = listQuery.data?.data ?? [];

  const deleteMutation = useMutation({
    mutationFn: (id: string) => adminApi.sources.xmltvDelete(id),
    onSuccess: () => {
      toast.success('Source deleted');
      qc.invalidateQueries({ queryKey: ['admin', 'sources', 'xmltv'] });
    },
    onError: (err: Error) => toast.error(`Delete failed: ${err.message}`),
  });

  const refreshMutation = useMutation({
    mutationFn: (id: string) => adminApi.sources.xmltvRefresh(id),
    onSuccess: () => {
      toast.success('Refresh started');
      setTimeout(
        () => qc.invalidateQueries({ queryKey: ['admin', 'sources', 'xmltv'] }),
        5_000,
      );
    },
    onError: (err: Error) => toast.error(`Refresh failed: ${err.message}`),
  });

  return (
    <SourcesPanel
      title="XMLTV guides"
      sources={sources}
      isLoading={listQuery.isPending}
      error={listQuery.error as Error | null}
      onAdd={() => setCreating(true)}
      onEdit={setEditing}
      onDelete={(id) => deleteMutation.mutate(id)}
      onRefresh={(id) => refreshMutation.mutate(id)}
      renderExtraColumn={(src) => (
        <td className="px-3 py-2 text-xs text-[color:var(--color-muted-foreground)]">
          {src.gzip ? 'gzip' : 'auto'}
        </td>
      )}
      extraColumnHeader="Gzip"
    >
      {editing ? (
        <SourceDrawer
          kind="xmltv"
          source={editing}
          onClose={() => setEditing(null)}
        />
      ) : null}
      {creating ? (
        <SourceDrawer
          kind="xmltv"
          source={null}
          onClose={() => setCreating(false)}
        />
      ) : null}
    </SourcesPanel>
  );
}

// ───────────────────────────── Shared table panel ─────────────────────────────

interface SourcesPanelProps<T extends AdminM3USource | AdminXMLTVSource> {
  title: string;
  sources: T[];
  isLoading: boolean;
  error: Error | null;
  onAdd: () => void;
  onEdit: (src: T) => void;
  onDelete: (id: string) => void;
  onRefresh: (id: string) => void;
  renderExtraColumn: ((src: T) => React.ReactNode) | null;
  extraColumnHeader?: string;
  children?: React.ReactNode; // drawer portal
}

function SourcesPanel<T extends AdminM3USource | AdminXMLTVSource>({
  title,
  sources,
  isLoading,
  error,
  onAdd,
  onEdit,
  onDelete,
  onRefresh,
  renderExtraColumn,
  extraColumnHeader,
  children,
}: SourcesPanelProps<T>) {
  const [confirming, setConfirming] = useState<T | null>(null);

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between">
        <h2 className="text-sm font-semibold uppercase tracking-wider text-[color:var(--color-muted-foreground)]">
          {title}
        </h2>
        <button
          type="button"
          onClick={onAdd}
          className="inline-flex items-center gap-1.5 rounded-md bg-amber-500 px-3 py-1.5 text-sm font-semibold text-black transition-colors hover:bg-amber-400"
        >
          <Plus size={14} /> Add source
        </button>
      </div>

      {isLoading ? (
        <div className="py-10 text-center text-sm text-[color:var(--color-muted-foreground)]">Loading…</div>
      ) : error ? (
        <div className="rounded-md border border-red-900/40 bg-red-950/30 p-4 text-sm text-red-300">
          Could not load sources: {error.message}
        </div>
      ) : sources.length === 0 ? (
        <div className="rounded-lg border border-dashed border-[color:var(--color-border)] p-10 text-center text-sm text-[color:var(--color-muted-foreground)]">
          No sources yet. Click <em>Add source</em> to configure the first one.
        </div>
      ) : (
        <div className="overflow-hidden rounded-lg border border-[color:var(--color-border)]">
          <table className="w-full border-collapse text-sm">
            <thead className="bg-[color:var(--color-surface)] text-[11px] uppercase tracking-wider text-[color:var(--color-muted-foreground)]">
              <tr>
                <th className="px-3 py-2 text-left font-semibold">Name</th>
                <th className="px-3 py-2 text-left font-semibold">URL</th>
                <th className="px-3 py-2 text-left font-semibold">Status</th>
                <th className="px-3 py-2 text-left font-semibold">Last refreshed</th>
                <th className="px-3 py-2 text-left font-semibold">Interval</th>
                <th className="px-3 py-2 text-left font-semibold">Enabled</th>
                {extraColumnHeader ? (
                  <th className="px-3 py-2 text-left font-semibold">{extraColumnHeader}</th>
                ) : null}
                <th className="px-3 py-2 text-right font-semibold">Actions</th>
              </tr>
            </thead>
            <tbody>
              {sources.map((src) => (
                <tr key={src.id} className="border-t border-[color:var(--color-border)] align-middle">
                  <td className="px-3 py-2 font-medium">{src.name}</td>
                  <td className="px-3 py-2 max-w-[28ch] truncate text-xs text-[color:var(--color-muted-foreground)]" title={src.url}>
                    {src.url}
                  </td>
                  <td className="px-3 py-2">
                    <StatusBadge status={src.last_status} />
                  </td>
                  <td
                    className="px-3 py-2 text-xs text-[color:var(--color-muted-foreground)]"
                    title={src.last_refreshed_at ? new Date(src.last_refreshed_at).toLocaleString() : ''}
                  >
                    {formatRelative(src.last_refreshed_at)}
                  </td>
                  <td className="px-3 py-2 font-mono text-xs tabular-nums">
                    {src.refresh_interval}
                  </td>
                  <td className="px-3 py-2">
                    <ReadOnlySwitch checked={src.enabled} />
                  </td>
                  {renderExtraColumn ? renderExtraColumn(src) : null}
                  <td className="px-3 py-2 text-right">
                    <div className="inline-flex items-center gap-1">
                      <IconButton title="Refresh now" onClick={() => onRefresh(src.id)}>
                        <RefreshCw size={14} />
                      </IconButton>
                      <IconButton title="Edit" onClick={() => onEdit(src)}>
                        <Pencil size={14} />
                      </IconButton>
                      <IconButton
                        title="Delete"
                        destructive
                        onClick={() => setConfirming(src)}
                      >
                        <Trash2 size={14} />
                      </IconButton>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <ConfirmDialog
        open={!!confirming}
        title="Delete source?"
        description={confirming ? `“${confirming.name}” will be removed. Channels imported from this source will also disappear.` : ''}
        confirmLabel="Delete"
        destructive
        onCancel={() => setConfirming(null)}
        onConfirm={() => {
          if (!confirming) return;
          onDelete(confirming.id);
          setConfirming(null);
        }}
      />

      {children}
    </div>
  );
}

function StatusBadge({ status }: { status?: string }) {
  if (!status) {
    return (
      <span className="rounded-full bg-[color:var(--color-surface)] px-2 py-0.5 text-[11px] text-[color:var(--color-muted-foreground)]">
        idle
      </span>
    );
  }
  const isError = status.toLowerCase().startsWith('error');
  const isOk = status.toLowerCase() === 'ok';
  return (
    <span
      title={status}
      className={cn(
        'rounded-full px-2 py-0.5 text-[11px]',
        isOk && 'bg-emerald-500/15 text-emerald-300',
        isError && 'bg-red-500/15 text-red-300',
        !isOk && !isError && 'bg-amber-500/15 text-amber-200',
      )}
    >
      {isError ? 'error' : isOk ? 'ok' : status}
    </span>
  );
}

function ReadOnlySwitch({ checked }: { checked: boolean }) {
  return (
    <span
      aria-label={checked ? 'Enabled' : 'Disabled'}
      className={cn(
        'inline-flex h-4 w-7 items-center rounded-full transition-colors',
        checked ? 'bg-emerald-500/60' : 'bg-zinc-700',
      )}
    >
      <span
        className={cn(
          'inline-block h-3 w-3 rounded-full bg-white transition-transform',
          checked ? 'translate-x-3.5' : 'translate-x-0.5',
        )}
      />
    </span>
  );
}

function IconButton({
  title,
  onClick,
  children,
  destructive,
}: {
  title: string;
  onClick: () => void;
  children: React.ReactNode;
  destructive?: boolean;
}) {
  return (
    <button
      type="button"
      title={title}
      onClick={onClick}
      className={cn(
        'rounded p-1.5 text-[color:var(--color-muted-foreground)] transition-colors',
        destructive
          ? 'hover:bg-red-500/20 hover:text-red-300'
          : 'hover:bg-[color:var(--color-surface-hover)] hover:text-[color:var(--color-foreground)]',
      )}
    >
      {children}
    </button>
  );
}

// ───────────────────────────── Source drawer (create/edit) ─────────────────────────────

interface DrawerProps {
  kind: 'm3u' | 'xmltv';
  source: AdminM3USource | AdminXMLTVSource | null; // null → create
  onClose: () => void;
}

function SourceDrawer({ kind, source, onClose }: DrawerProps) {
  const qc = useQueryClient();
  const queryKey = ['admin', 'sources', kind];
  const isXMLTV = kind === 'xmltv';

  const [name, setName] = useState(source?.name ?? '');
  const [url, setUrl] = useState(source?.url ?? '');
  const [headersText, setHeadersText] = useState(
    source?.http_headers && Object.keys(source.http_headers).length > 0
      ? JSON.stringify(source.http_headers, null, 2)
      : '',
  );
  const [enabled, setEnabled] = useState(source?.enabled ?? true);
  const [refreshInterval, setRefreshInterval] = useState(source?.refresh_interval ?? '6h');
  const [gzip, setGzip] = useState(isXMLTV && source ? (source as AdminXMLTVSource).gzip : false);
  const [headerError, setHeaderError] = useState<string | null>(null);

  // Reset header error when text changes so the user gets fast feedback.
  useEffect(() => {
    setHeaderError(null);
  }, [headersText]);

  const parsedHeaders = useMemo<Record<string, string> | null>(() => {
    if (!headersText.trim()) return {};
    try {
      const obj = JSON.parse(headersText);
      if (!obj || typeof obj !== 'object' || Array.isArray(obj)) return null;
      const out: Record<string, string> = {};
      for (const [k, v] of Object.entries(obj)) {
        if (typeof v !== 'string') return null;
        out[k] = v;
      }
      return out;
    } catch {
      return null;
    }
  }, [headersText]);

  const intervalValid = isLikelyDuration(refreshInterval);

  const saveMutation = useMutation({
    mutationFn: async () => {
      if (parsedHeaders == null) {
        throw new Error('HTTP headers must be a JSON object of string values');
      }
      if (!intervalValid) {
        throw new Error('Refresh interval must be a Go duration like "6h" or "3h30m"');
      }
      const m3uPayload: AdminM3USourceInput = {
        name: name.trim(),
        url: url.trim(),
        http_headers: parsedHeaders,
        enabled,
        refresh_interval: refreshInterval.trim(),
      };
      if (kind === 'm3u') {
        return source
          ? adminApi.sources.m3uUpdate(source.id, m3uPayload)
          : adminApi.sources.m3uCreate(m3uPayload);
      }
      const xmltvPayload: AdminXMLTVSourceInput = { ...m3uPayload, gzip };
      return source
        ? adminApi.sources.xmltvUpdate(source.id, xmltvPayload)
        : adminApi.sources.xmltvCreate(xmltvPayload);
    },
    onSuccess: () => {
      toast.success(source ? 'Source saved' : 'Source created');
      qc.invalidateQueries({ queryKey });
      onClose();
    },
    onError: (err: Error) => toast.error(err.message),
  });

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    if (parsedHeaders == null) {
      setHeaderError('Must be a JSON object of string values');
      return;
    }
    saveMutation.mutate();
  };

  return (
    <Dialog.Root open onOpenChange={(o) => { if (!o) onClose(); }}>
      <Dialog.Portal>
        <Dialog.Overlay className="fixed inset-0 z-40 bg-black/60 backdrop-blur-sm" />
        <Dialog.Content className="fixed right-0 top-0 z-50 flex h-dvh w-full max-w-md flex-col overflow-y-auto border-l border-[color:var(--color-border)] bg-[color:var(--color-background)] shadow-xl">
          <header className="flex items-center justify-between border-b border-[color:var(--color-border)] px-5 py-3">
            <Dialog.Title className="text-sm font-semibold">
              {source ? 'Edit source' : 'Add source'}
            </Dialog.Title>
            <Dialog.Close
              className="rounded p-1 text-[color:var(--color-muted-foreground)] hover:bg-[color:var(--color-surface)]"
              aria-label="Close"
            >
              <XIcon size={16} />
            </Dialog.Close>
          </header>

          <form onSubmit={submit} className="flex flex-1 flex-col gap-4 px-5 py-4">
            <Field label="Name">
              <input
                value={name}
                onChange={(e) => setName(e.target.value)}
                required
                placeholder="My provider"
                className="form-input"
              />
            </Field>
            <Field label="URL">
              <input
                value={url}
                onChange={(e) => setUrl(e.target.value)}
                required
                type="url"
                placeholder="https://example.com/playlist.m3u"
                className="form-input font-mono text-xs"
              />
            </Field>
            <Field
              label="HTTP headers"
              hint='JSON object, e.g. {"User-Agent":"VLC/3.0"}. Leave blank for none.'
              error={headerError ?? (parsedHeaders == null && headersText.trim() ? 'Must be a JSON object of string values' : null)}
            >
              <textarea
                value={headersText}
                onChange={(e) => setHeadersText(e.target.value)}
                rows={4}
                placeholder="{}"
                className="form-input min-h-[5rem] font-mono text-xs"
              />
            </Field>
            <Field
              label="Refresh interval"
              hint='Go duration string. e.g. "6h", "3h30m", "45m".'
              error={!intervalValid && refreshInterval ? 'Not a recognisable duration' : null}
            >
              <input
                value={refreshInterval}
                onChange={(e) => setRefreshInterval(e.target.value)}
                required
                placeholder="6h"
                className="form-input font-mono"
              />
            </Field>
            <div className="flex items-center justify-between">
              <span className="text-sm">Enabled</span>
              <ToggleSwitch checked={enabled} onChange={setEnabled} />
            </div>
            {isXMLTV ? (
              <div className="flex items-start justify-between gap-3">
                <div>
                  <div className="text-sm">Force gzip</div>
                  <div className="text-xs text-[color:var(--color-muted-foreground)]">
                    Leave off for auto-detect via Content-Encoding.
                  </div>
                </div>
                <ToggleSwitch checked={gzip} onChange={setGzip} />
              </div>
            ) : null}

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
                {saveMutation.isPending ? 'Saving…' : source ? 'Save' : 'Create'}
              </button>
            </div>
          </form>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}

// ───────────────────────────── Shared form bits ─────────────────────────────

export function Field({
  label,
  hint,
  error,
  children,
}: {
  label: string;
  hint?: string;
  error?: string | null;
  children: React.ReactNode;
}) {
  return (
    <label className="flex flex-col gap-1 text-sm">
      <span className="text-[color:var(--color-foreground)]">{label}</span>
      {children}
      {error ? (
        <span className="text-[11px] text-red-400">{error}</span>
      ) : hint ? (
        <span className="text-[11px] text-[color:var(--color-muted-foreground)]">{hint}</span>
      ) : null}
    </label>
  );
}

export function ToggleSwitch({
  checked,
  onChange,
}: {
  checked: boolean;
  onChange: (next: boolean) => void;
}) {
  return (
    <Switch.Root
      checked={checked}
      onCheckedChange={onChange}
      className={cn(
        'relative inline-flex h-5 w-9 shrink-0 items-center rounded-full transition-colors',
        checked ? 'bg-emerald-500/70' : 'bg-zinc-700',
      )}
    >
      <Switch.Thumb className="block h-4 w-4 translate-x-0.5 rounded-full bg-white transition-transform data-[state=checked]:translate-x-[18px]" />
    </Switch.Root>
  );
}

export function ConfirmDialog({
  open,
  title,
  description,
  confirmLabel,
  destructive,
  onCancel,
  onConfirm,
}: {
  open: boolean;
  title: string;
  description: string;
  confirmLabel: string;
  destructive?: boolean;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  return (
    <Dialog.Root open={open} onOpenChange={(o) => { if (!o) onCancel(); }}>
      <Dialog.Portal>
        <Dialog.Overlay className="fixed inset-0 z-40 bg-black/60 backdrop-blur-sm" />
        <Dialog.Content className="fixed left-1/2 top-1/2 z-50 w-[calc(100%-2rem)] max-w-sm -translate-x-1/2 -translate-y-1/2 rounded-lg border border-[color:var(--color-border)] bg-[color:var(--color-background)] p-5 shadow-xl">
          <Dialog.Title className="text-sm font-semibold">{title}</Dialog.Title>
          <Dialog.Description className="mt-2 text-sm text-[color:var(--color-muted-foreground)]">
            {description}
          </Dialog.Description>
          <div className="mt-5 flex items-center justify-end gap-2">
            <button
              type="button"
              onClick={onCancel}
              className="rounded-md border border-[color:var(--color-border)] px-3 py-1.5 text-sm hover:bg-[color:var(--color-surface)]"
            >
              Cancel
            </button>
            <button
              type="button"
              onClick={onConfirm}
              className={cn(
                'rounded-md px-3 py-1.5 text-sm font-semibold',
                destructive
                  ? 'bg-red-500/80 text-white hover:bg-red-500'
                  : 'bg-amber-500 text-black hover:bg-amber-400',
              )}
            >
              {confirmLabel}
            </button>
          </div>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
