import { useMemo } from 'react';
import { Dialog } from 'radix-ui';
import { useNavigate, useParams } from 'react-router';
import { useMutation, useQuery } from '@tanstack/react-query';
import { XIcon, Play } from 'lucide-react';
import { toast } from 'sonner';
import { api, type Program, type Channel } from '@/api/client';
import { formatTimeRange, isAiringNow } from '@/lib/utils';

interface Props {
  // Page mode renders the program detail as a full-page route at
  // /programs/:id (entered by direct navigation). Modal mode renders an
  // overlay on top of whatever route is in state.background (entered from
  // the guide or now/next panel via navigate(..., {state: {background}})).
  mode: 'page' | 'modal';
}

// ProgramDetailRoute is the entry component for both rendering modes. It
// reads the :id param, fires the detail query, and dispatches to one of
// two layouts. The two layouts share the inner ProgramBody so any edits
// to fields/credits propagate to both.
export function ProgramDetailRoute({ mode }: Props) {
  const { id = '' } = useParams();
  const navigate = useNavigate();

  const programQuery = useQuery({
    queryKey: ['program', id],
    queryFn: () => api.program(id),
    enabled: !!id,
  });

  const handleClose = () => {
    // Pop back to whatever the previous route was — works for both modal
    // overlays (returns to the guide) and page-mode visits (returns to the
    // referrer or, failing that, to /guide).
    if (window.history.length > 1) navigate(-1);
    else navigate('/guide');
  };

  if (mode === 'modal') {
    return (
      <Dialog.Root open onOpenChange={(open) => { if (!open) handleClose(); }}>
        <Dialog.Portal>
          <Dialog.Overlay className="fixed inset-0 z-40 bg-black/60 backdrop-blur-sm" />
          <Dialog.Content
            className="fixed left-1/2 top-1/2 z-50 max-h-[calc(100dvh-4rem)] w-[calc(100%-2rem)] -translate-x-1/2 -translate-y-1/2 overflow-y-auto rounded-lg border border-[color:var(--color-border)] bg-[color:var(--color-background)] p-6 shadow-xl sm:max-w-2xl"
          >
            <Dialog.Title className="sr-only">Program details</Dialog.Title>
            <Dialog.Close
              className="absolute right-4 top-4 rounded p-1 text-[color:var(--color-muted-foreground)] hover:bg-[color:var(--color-surface)]"
              aria-label="Close"
            >
              <XIcon size={18} />
            </Dialog.Close>
            <ProgramBody
              loading={programQuery.isPending}
              error={programQuery.error as Error | null}
              program={programQuery.data}
              onClose={handleClose}
            />
          </Dialog.Content>
        </Dialog.Portal>
      </Dialog.Root>
    );
  }

  // Page mode: full-page detail (typically hit via a direct link).
  return (
    <div className="mx-auto max-w-2xl rounded-lg border border-[color:var(--color-border)] bg-[color:var(--color-surface)] p-6">
      <ProgramBody
        loading={programQuery.isPending}
        error={programQuery.error as Error | null}
        program={programQuery.data}
        onClose={handleClose}
      />
    </div>
  );
}

// ProgramBody is the shared content layout between modal and page modes.
// Renders title, sub-title, formatted time range, description, categories,
// credits grouped by kind, and a "Play now" button when the program is
// currently airing AND we can resolve a channel for it.
function ProgramBody({
  loading,
  error,
  program,
  onClose,
}: {
  loading: boolean;
  error: Error | null;
  program: Program | undefined;
  onClose: () => void;
}) {
  if (loading) {
    return <div className="text-sm text-[color:var(--color-muted-foreground)]">Loading…</div>;
  }
  if (error) {
    return (
      <div className="rounded-md border border-red-900/40 bg-red-950/30 p-4 text-sm text-red-300">
        Could not load program: {error.message}
      </div>
    );
  }
  if (!program) return null;

  const live = isAiringNow(program.start, program.stop);
  return (
    <div className="space-y-4">
      <div>
        <h2 className="text-xl font-semibold leading-tight">{program.title}</h2>
        {program.sub_title ? (
          <p className="mt-0.5 text-sm text-[color:var(--color-muted-foreground)]">{program.sub_title}</p>
        ) : null}
      </div>

      <div className="flex flex-wrap items-center gap-3 text-xs text-[color:var(--color-muted-foreground)]">
        <span className="font-mono tabular-nums">{formatTimeRange(program.start, program.stop)}</span>
        {program.rating ? <span className="rounded bg-black/40 px-1.5 py-0.5">{program.rating}</span> : null}
        {program.episode_num ? <span>Ep. {program.episode_num}</span> : null}
        {program.season_num != null && program.episode != null ? (
          <span>
            S{program.season_num}·E{program.episode}
          </span>
        ) : null}
        {program.original_air_date ? (
          <span>Aired {new Date(program.original_air_date).toLocaleDateString()}</span>
        ) : null}
      </div>

      {program.categories?.length ? (
        <div className="flex flex-wrap gap-1.5">
          {program.categories.map((c) => (
            <span
              key={c}
              className="rounded-full border border-[color:var(--color-border)] bg-[color:var(--color-surface)] px-2 py-0.5 text-[11px] text-[color:var(--color-muted-foreground)]"
            >
              {c}
            </span>
          ))}
        </div>
      ) : null}

      {program.description ? (
        <p className="whitespace-pre-line text-sm leading-relaxed text-[color:var(--color-foreground)]/90">
          {program.description}
        </p>
      ) : null}

      <CreditsBlock program={program} />

      <div className="flex items-center gap-2 pt-2">
        {live ? <PlayNowButton program={program} onPlay={onClose} /> : null}
      </div>
    </div>
  );
}

// CreditsBlock groups credits by kind (director, actor, writer…) and
// renders one chip-style cluster per group. Empty when the API didn't
// hydrate credits (the guide window strips them to keep the response tiny).
function CreditsBlock({ program }: { program: Program }) {
  const groups = useMemo(() => {
    const acc: Record<string, string[]> = {};
    for (const c of program.credits ?? []) {
      (acc[c.kind] ??= []).push(c.name);
    }
    return Object.entries(acc);
  }, [program]);

  if (groups.length === 0) return null;
  return (
    <div className="space-y-2 border-t border-[color:var(--color-border)] pt-3">
      {groups.map(([kind, names]) => (
        <div key={kind} className="flex flex-col gap-1 sm:flex-row sm:gap-3">
          <span className="w-24 shrink-0 text-xs font-semibold uppercase tracking-wider text-[color:var(--color-muted-foreground)]">
            {kind}
          </span>
          <span className="text-sm">{names.join(', ')}</span>
        </div>
      ))}
    </div>
  );
}

// PlayNowButton resolves the program → channel via the program's
// channel_id (set by the guide handler) or, failing that, falls back to
// xmltv_channel_id by scanning the cached channels list. Once it has a
// channel id, it mints a stream session and navigates to /watch/:id.
function PlayNowButton({ program, onPlay }: { program: Program; onPlay: () => void }) {
  const navigate = useNavigate();

  // If the program has channel_id (guide query) we use it directly. For
  // search-results the only link is xmltv_channel_id, so we look that up
  // in the channels list to translate to the internal channel id.
  const channelsQuery = useQuery({
    queryKey: ['channels', { forResolve: true }],
    queryFn: () => api.channels({ limit: 500 }),
    enabled: !program.channel_id && !!program.xmltv_channel_id,
  });

  const resolved = useMemo<Channel | { id: string } | null>(() => {
    if (program.channel_id) return { id: program.channel_id };
    const all = channelsQuery.data?.data ?? [];
    return (
      all.find(
        (c) =>
          c.current_program?.id === program.id ||
          c.next_program?.id === program.id,
      ) ?? null
    );
  }, [program, channelsQuery.data]);

  const play = useMutation({
    mutationFn: async () => {
      if (!resolved) throw new Error('No channel matched this program.');
      const session = await api.startStream(resolved.id);
      return { channelId: resolved.id, session };
    },
    onSuccess: ({ channelId }) => {
      onPlay();
      navigate(`/watch/${encodeURIComponent(channelId)}`);
    },
    onError: (err) => toast.error(`Couldn't start stream: ${(err as Error).message}`),
  });

  if (!resolved && !channelsQuery.isPending) return null;

  return (
    <button
      type="button"
      onClick={() => play.mutate()}
      disabled={play.isPending || !resolved}
      className="inline-flex items-center gap-1.5 rounded-md bg-amber-500 px-3 py-1.5 text-sm font-semibold text-black transition-colors hover:bg-amber-400 disabled:opacity-60"
    >
      <Play size={14} fill="currentColor" /> Play now
    </button>
  );
}
