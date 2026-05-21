import { useEffect, useMemo } from 'react';
import { useNavigate, useLocation } from 'react-router';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { api, type Channel } from '@/api/client';
import { formatTimeRange, isAiringNow } from '@/lib/utils';

interface Props {
  channel: Channel;
}

// NowNextPanel shows the schedule for the channel currently playing —
// a 2-hour look-ahead window starting from now. Clicking a program opens
// its detail modal in front of the player page.
const LOOKAHEAD_HOURS = 2;

export function NowNextPanel({ channel }: Props) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const location = useLocation();

  const { windowStart, windowEnd } = useMemo(() => {
    const start = new Date();
    const end = new Date(start.getTime() + LOOKAHEAD_HOURS * 3600_000);
    return { windowStart: start.toISOString(), windowEnd: end.toISOString() };
  }, []);

  const guideQuery = useQuery({
    queryKey: ['guide', { panel: 'now-next', channel: channel.id, windowStart, windowEnd }],
    queryFn: () => api.guide(windowStart, windowEnd, { channels: [channel.id] }),
  });

  // Same next-minute-boundary refresh pattern as the main guide so the
  // currently-airing row keeps its highlight without polling.
  useEffect(() => {
    const now = new Date();
    const ms = 60000 - (now.getTime() % 60000);
    const t = setTimeout(
      () => qc.invalidateQueries({ queryKey: ['guide', { panel: 'now-next', channel: channel.id }] }),
      ms,
    );
    return () => clearTimeout(t);
  }, [guideQuery.data, channel.id, qc]);

  const programs = guideQuery.data?.data?.[channel.id] ?? [];

  return (
    <aside className="rounded-lg border border-[color:var(--color-border)] bg-[color:var(--color-surface)] p-3">
      <h2 className="mb-2 text-xs font-semibold uppercase tracking-wider text-[color:var(--color-muted-foreground)]">
        On {channel.display_name}
      </h2>
      {guideQuery.isPending ? (
        <div className="text-sm text-[color:var(--color-muted-foreground)]">Loading schedule…</div>
      ) : programs.length === 0 ? (
        <div className="text-sm text-[color:var(--color-muted-foreground)]">No upcoming entries.</div>
      ) : (
        <ul className="space-y-1">
          {programs.slice(0, 10).map((p) => {
            const live = isAiringNow(p.start, p.stop);
            return (
              <li key={p.id}>
                <button
                  type="button"
                  onClick={() =>
                    navigate(`/programs/${encodeURIComponent(p.id)}`, { state: { background: location } })
                  }
                  className={`w-full rounded-md border p-2 text-left transition-colors ${
                    live
                      ? 'border-amber-500/40 bg-amber-500/10 hover:bg-amber-500/20'
                      : 'border-transparent hover:bg-[color:var(--color-surface-hover)]'
                  }`}
                >
                  <div className="flex items-baseline justify-between gap-2">
                    <span className="truncate text-sm font-medium">{p.title}</span>
                    {live ? (
                      <span className="shrink-0 rounded bg-amber-500/30 px-1.5 text-[10px] font-medium text-amber-100">
                        LIVE
                      </span>
                    ) : null}
                  </div>
                  <div className="font-mono text-[11px] text-[color:var(--color-muted-foreground)]">
                    {formatTimeRange(p.start, p.stop)}
                  </div>
                </button>
              </li>
            );
          })}
        </ul>
      )}
    </aside>
  );
}
