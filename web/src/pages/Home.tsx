import { useMemo } from 'react';
import { Link, useLocation, useNavigate } from 'react-router';
import { useQuery } from '@tanstack/react-query';
import { api, type Channel, type Program } from '@/api/client';
import { Rail } from '@/components/Rail';
import { ChannelCard } from '@/components/ChannelCard';
import { cn, formatTimeRange, isAiringNow } from '@/lib/utils';

// Home page composes three rails:
//   1. Recently watched — api.recent() joined with channel detail.
//   2. Favorites — api.favorites() joined with channel detail (in user order).
//   3. On now across favorites — guide(now, now+2h) filtered to favorite ids,
//      flattened to the currently-airing program per channel.
//
// All channel-detail joins go through a single api.channels({ limit: 500 })
// query so the rails share one cache entry and Home renders in a single
// network round trip for the steady state.
export function Home() {
  const recentQuery = useQuery({ queryKey: ['recent'], queryFn: () => api.recent() });
  const favoritesQuery = useQuery({ queryKey: ['favorites'], queryFn: () => api.favorites() });
  const channelsQuery = useQuery({
    queryKey: ['channels', { forHome: true }],
    queryFn: () => api.channels({ limit: 500 }),
  });

  const favIds = useMemo(
    () => favoritesQuery.data?.data.map((f) => f.channel_id) ?? [],
    [favoritesQuery.data],
  );

  // On-now panel runs a 2h guide query scoped to favourites only so the
  // payload stays small even with hundreds of channels in the library.
  const { onNowStart, onNowEnd } = useMemo(() => {
    const now = new Date();
    return {
      onNowStart: now.toISOString(),
      onNowEnd: new Date(now.getTime() + 2 * 3600_000).toISOString(),
    };
  }, []);

  const onNowGuide = useQuery({
    queryKey: ['guide', { home: true, channels: favIds, onNowStart, onNowEnd }],
    queryFn: () =>
      api.guide(onNowStart, onNowEnd, { channels: favIds.length ? favIds : undefined }),
    enabled: favIds.length > 0,
  });

  const channelsById = useMemo(() => {
    const map = new Map<string, Channel>();
    for (const c of channelsQuery.data?.data ?? []) map.set(c.id, c);
    return map;
  }, [channelsQuery.data]);

  // Map favorites to channels respecting their saved position.
  const favoriteChannels = useMemo(
    () =>
      favIds
        .map((id) => channelsById.get(id))
        .filter((c): c is Channel => !!c),
    [favIds, channelsById],
  );

  // Recents come back in last-tuned-first order from the API.
  const recentChannels = useMemo(() => {
    const items = recentQuery.data?.data ?? [];
    return items
      .map((r) => channelsById.get(r.channel_id))
      .filter((c): c is Channel => !!c);
  }, [recentQuery.data, channelsById]);

  // Flatten the on-now guide into one entry per channel: the program that
  // is currently airing (or the next one if nothing is mid-air right now).
  const onNowEntries = useMemo(() => {
    const data = onNowGuide.data?.data ?? {};
    const list: Array<{ channel: Channel; program: Program }> = [];
    for (const channel of favoriteChannels) {
      const programs = data[channel.id] ?? [];
      const live = programs.find((p) => isAiringNow(p.start, p.stop));
      const pick = live ?? programs[0];
      if (pick) list.push({ channel, program: pick });
    }
    return list;
  }, [onNowGuide.data, favoriteChannels]);

  return (
    <div className="space-y-8">
      <Rail
        title="Recently watched"
        empty={
          recentChannels.length === 0 && !recentQuery.isPending ? (
            <EmptyHint
              text="Watch a few channels and they'll appear here for quick access."
              linkText="Browse channels"
              to="/channels"
            />
          ) : null
        }
      >
        {recentChannels.map((c) => (
          <RailCard key={c.id} channel={c} />
        ))}
      </Rail>

      <Rail
        title="Favorites"
        empty={
          favoriteChannels.length === 0 && !favoritesQuery.isPending ? (
            <EmptyHint
              text="Star a channel to pin it here."
              linkText="Open Channels"
              to="/channels"
            />
          ) : null
        }
      >
        {favoriteChannels.map((c) => (
          <RailCard key={c.id} channel={c} />
        ))}
      </Rail>

      <Rail
        title="On now"
        empty={
          favoriteChannels.length === 0 ? (
            <EmptyHint
              text="Star a channel to see what's on now."
              linkText="Open Channels"
              to="/channels"
            />
          ) : onNowEntries.length === 0 && !onNowGuide.isPending ? (
            <div className="rounded-lg border border-dashed border-[color:var(--color-border)] px-4 py-3 text-xs text-[color:var(--color-muted-foreground)]">
              Nothing currently scheduled on your favorites.
            </div>
          ) : null
        }
      >
        {onNowEntries.map(({ channel, program }) => (
          <ProgramRailCard key={`${channel.id}-${program.id}`} channel={channel} program={program} />
        ))}
      </Rail>
    </div>
  );
}

// RailCard is a fixed-width wrapper around ChannelCard so the horizontal
// scroll has predictable item widths. Using ChannelCard directly keeps the
// favorite-star and watch-link behaviour identical to the grid page.
function RailCard({ channel }: { channel: Channel }) {
  return (
    <div className="w-64 shrink-0">
      <ChannelCard channel={channel} />
    </div>
  );
}

// ProgramRailCard surfaces the program first, with the channel name below.
// Clicking opens the ProgramDetail modal so users can decide to play or
// just inspect — the channel logo/name is the secondary affordance.
function ProgramRailCard({ channel, program }: { channel: Channel; program: Program }) {
  const navigate = useNavigate();
  const location = useLocation();
  const live = isAiringNow(program.start, program.stop);
  return (
    <button
      type="button"
      onClick={() =>
        navigate(`/programs/${encodeURIComponent(program.id)}`, { state: { background: location } })
      }
      className={cn(
        'flex w-72 shrink-0 flex-col gap-2 rounded-lg border p-3 text-left transition-colors',
        live
          ? 'border-amber-500/40 bg-amber-500/10 hover:bg-amber-500/20'
          : 'border-[color:var(--color-border)] bg-[color:var(--color-surface)] hover:bg-[color:var(--color-surface-hover)]',
      )}
    >
      <div className="flex items-center gap-2">
        <div className="flex h-8 w-8 shrink-0 items-center justify-center overflow-hidden rounded bg-black/40">
          {channel.logo_url ? (
            <img src={channel.logo_url} alt="" className="max-h-full max-w-full object-contain" />
          ) : null}
        </div>
        <span className="truncate text-xs text-[color:var(--color-muted-foreground)]">
          {channel.display_name}
        </span>
        {live ? (
          <span className="ml-auto rounded bg-amber-500/30 px-1.5 text-[10px] font-medium text-amber-100">
            LIVE
          </span>
        ) : null}
      </div>
      <div>
        <div className="line-clamp-2 text-sm font-medium">{program.title}</div>
        <div className="mt-0.5 font-mono text-[11px] text-[color:var(--color-muted-foreground)]">
          {formatTimeRange(program.start, program.stop)}
        </div>
      </div>
    </button>
  );
}

function EmptyHint({ text, linkText, to }: { text: string; linkText: string; to: string }) {
  return (
    <div className="rounded-lg border border-dashed border-[color:var(--color-border)] px-4 py-3 text-xs text-[color:var(--color-muted-foreground)]">
      {text}{' '}
      <Link className="underline" to={to}>
        {linkText}
      </Link>
      .
    </div>
  );
}
