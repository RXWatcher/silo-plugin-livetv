import { useMemo, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Tv } from 'lucide-react';
import { toast } from 'sonner';
import { adminApi, type AdminSession } from '@/api/admin';
import { api, type Channel } from '@/api/client';
import { formatBytes, formatRelative } from '@/lib/utils';
import { ConfirmDialog } from './Sources';

// Sessions admin page: a live list of active stream sessions with a kill
// action per row. Auto-refreshes every 30 seconds so the page stays fresh
// without operator intervention. Idle-for is computed client-side from
// last_byte_at so we don't burn a server round-trip every second.
export function Sessions() {
  const qc = useQueryClient();
  const [confirming, setConfirming] = useState<AdminSession | null>(null);

  const sessionsQuery = useQuery({
    queryKey: ['admin', 'sessions'],
    queryFn: () => adminApi.sessions.list(),
    refetchInterval: 30_000,
  });

  // Channels lookup is used to render display_name + logo. The admin page
  // doesn't have its own channel-details endpoint, so we lean on the user
  // /channels list (limit 500 is enough for any sane install). Cached for
  // 30s along with the rest.
  const channelsQuery = useQuery({
    queryKey: ['channels', { forSessionsAdmin: true }],
    queryFn: () => api.channels({ limit: 500 }),
  });

  const channelsById = useMemo(() => {
    const m = new Map<string, Channel>();
    for (const c of channelsQuery.data?.data ?? []) m.set(c.id, c);
    return m;
  }, [channelsQuery.data]);

  const killMutation = useMutation({
    mutationFn: (id: string) => adminApi.sessions.kill(id),
    onMutate: async (id) => {
      // Optimistically drop the row so the operator sees the kill take
      // effect immediately. The server is idempotent so retrying is safe.
      await qc.cancelQueries({ queryKey: ['admin', 'sessions'] });
      const previous = qc.getQueryData<{ data: AdminSession[] }>(['admin', 'sessions']);
      if (previous) {
        qc.setQueryData(['admin', 'sessions'], {
          ...previous,
          data: previous.data.filter((s) => s.id !== id),
        });
      }
      return { previous };
    },
    onError: (err: Error, _id, ctx) => {
      if (ctx?.previous) qc.setQueryData(['admin', 'sessions'], ctx.previous);
      toast.error(`Couldn't end session: ${err.message}`);
    },
    onSuccess: () => {
      toast.success('Session ended');
      qc.invalidateQueries({ queryKey: ['admin', 'sessions'] });
    },
  });

  const sessions = sessionsQuery.data?.data ?? [];

  return (
    <div className="space-y-4">
      <header className="flex items-center justify-between">
        <h1 className="text-lg font-semibold tracking-tight">Sessions</h1>
        <p className="text-xs text-[color:var(--color-muted-foreground)]">
          Refreshes every 30s. {sessions.length} active.
        </p>
      </header>

      {sessionsQuery.isPending ? (
        <div className="py-10 text-center text-sm text-[color:var(--color-muted-foreground)]">Loading sessions…</div>
      ) : sessionsQuery.isError ? (
        <div className="rounded-md border border-red-900/40 bg-red-950/30 p-4 text-sm text-red-300">
          Could not load sessions: {(sessionsQuery.error as Error).message}
        </div>
      ) : sessions.length === 0 ? (
        <div className="rounded-lg border border-dashed border-[color:var(--color-border)] p-10 text-center text-sm text-[color:var(--color-muted-foreground)]">
          No active sessions.
        </div>
      ) : (
        <div className="overflow-auto rounded-lg border border-[color:var(--color-border)]">
          <table className="w-full border-collapse text-sm">
            <thead className="bg-[color:var(--color-surface)] text-[11px] uppercase tracking-wider text-[color:var(--color-muted-foreground)]">
              <tr>
                <th className="px-3 py-2 text-left font-semibold">Session</th>
                <th className="px-3 py-2 text-left font-semibold">User</th>
                <th className="px-3 py-2 text-left font-semibold">Channel</th>
                <th className="px-3 py-2 text-left font-semibold">Started</th>
                <th className="px-3 py-2 text-left font-semibold">Idle for</th>
                <th className="px-3 py-2 text-left font-semibold">Bytes</th>
                <th className="px-3 py-2 text-left font-semibold">Client</th>
                <th className="px-3 py-2 text-right font-semibold">Actions</th>
              </tr>
            </thead>
            <tbody>
              {sessions.map((s) => (
                <SessionRow
                  key={s.id}
                  session={s}
                  channel={channelsById.get(s.channel_id)}
                  onKill={() => setConfirming(s)}
                />
              ))}
            </tbody>
          </table>
        </div>
      )}

      <ConfirmDialog
        open={!!confirming}
        title="End this session?"
        description={
          confirming
            ? `Session ${confirming.id.slice(0, 8)}… will be killed. The user's playback will stop.`
            : ''
        }
        confirmLabel="End session"
        destructive
        onCancel={() => setConfirming(null)}
        onConfirm={() => {
          if (!confirming) return;
          killMutation.mutate(confirming.id);
          setConfirming(null);
        }}
      />
    </div>
  );
}

function SessionRow({
  session,
  channel,
  onKill,
}: {
  session: AdminSession;
  channel: Channel | undefined;
  onKill: () => void;
}) {
  return (
    <tr className="border-t border-[color:var(--color-border)] align-middle">
      <td className="px-3 py-2">
        <span
          title={session.id}
          className="font-mono text-[11px] text-[color:var(--color-muted-foreground)]"
        >
          {session.id.slice(0, 12)}…
        </span>
      </td>
      <td className="px-3 py-2 font-mono text-[11px] text-[color:var(--color-muted-foreground)]">
        {session.user_id.slice(0, 12)}…
      </td>
      <td className="px-3 py-2">
        <div className="flex items-center gap-2">
          <div className="flex h-7 w-7 shrink-0 items-center justify-center overflow-hidden rounded bg-black/40">
            {channel?.logo_url ? (
              <img
                src={channel.logo_url}
                alt=""
                className="max-h-full max-w-full object-contain"
                loading="lazy"
              />
            ) : (
              <Tv size={12} className="text-[color:var(--color-muted-foreground)]" />
            )}
          </div>
          <div className="min-w-0">
            <div className="truncate text-sm">{channel?.display_name ?? session.channel_id}</div>
          </div>
        </div>
      </td>
      <td
        className="px-3 py-2 text-xs text-[color:var(--color-muted-foreground)]"
        title={new Date(session.started_at).toLocaleString()}
      >
        {formatRelative(session.started_at)}
      </td>
      <td
        className="px-3 py-2 text-xs text-[color:var(--color-muted-foreground)]"
        title={new Date(session.last_byte_at).toLocaleString()}
      >
        {formatRelative(session.last_byte_at)}
      </td>
      <td className="px-3 py-2 font-mono text-xs tabular-nums">
        {formatBytes(session.bytes_streamed)}
      </td>
      <td className="px-3 py-2 text-xs text-[color:var(--color-muted-foreground)]">
        {session.client_ip ? (
          <span className="font-mono">{session.client_ip}</span>
        ) : session.user_agent ? (
          <span title={session.user_agent} className="block max-w-[18ch] truncate">
            {session.user_agent}
          </span>
        ) : (
          '—'
        )}
      </td>
      <td className="px-3 py-2 text-right">
        <button
          type="button"
          onClick={onKill}
          className="rounded-md border border-red-900/40 px-2.5 py-1 text-xs text-red-300 hover:bg-red-500/15"
        >
          Kill
        </button>
      </td>
    </tr>
  );
}
