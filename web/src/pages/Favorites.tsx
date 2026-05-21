import { useEffect, useMemo, useState } from 'react';
import {
  DndContext,
  PointerSensor,
  KeyboardSensor,
  closestCenter,
  useSensor,
  useSensors,
  type DragEndEvent,
} from '@dnd-kit/core';
import {
  SortableContext,
  arrayMove,
  rectSortingStrategy,
  sortableKeyboardCoordinates,
  useSortable,
} from '@dnd-kit/sortable';
import { CSS } from '@dnd-kit/utilities';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Star, GripVertical } from 'lucide-react';
import { toast } from 'sonner';
import { Link } from 'react-router';
import { api, type Channel, type Favorite } from '@/api/client';

// Favorites page lets the user drag-reorder their starred channels. The
// ordering is persisted on every drop via api.reorderFav with optimistic
// updates — onMutate snapshots the previous order, onError restores it,
// and onSuccess re-fetches to reconcile any server-side normalisation.
export function Favorites() {
  const qc = useQueryClient();

  const favoritesQuery = useQuery({
    queryKey: ['favorites'],
    queryFn: () => api.favorites(),
  });

  // We need channel detail (logo, name, current program) for each favorite
  // — there's no dedicated endpoint that joins favorites→channels, so we
  // pull a wide channels list and look them up in memory. This is cheap;
  // most installations have a few hundred channels at most.
  const channelsQuery = useQuery({
    queryKey: ['channels', { forFavorites: true }],
    queryFn: () => api.channels({ limit: 500 }),
  });

  // Local order mirrors the server until the user drags; on every drop we
  // update local then dispatch the mutation. We keep state local because
  // dnd-kit needs a stable, ordered id list across renders.
  const [orderedIds, setOrderedIds] = useState<string[]>([]);
  useEffect(() => {
    if (!favoritesQuery.data) return;
    setOrderedIds(favoritesQuery.data.data.map((f) => f.channel_id));
  }, [favoritesQuery.data]);

  const channelsById = useMemo(() => {
    const map = new Map<string, Channel>();
    for (const c of channelsQuery.data?.data ?? []) map.set(c.id, c);
    return map;
  }, [channelsQuery.data]);

  const reorderMutation = useMutation({
    mutationFn: (ids: string[]) => api.reorderFav(ids),
    onMutate: async (ids) => {
      await qc.cancelQueries({ queryKey: ['favorites'] });
      const previous = qc.getQueryData<{ data: Favorite[]; next_cursor?: string }>(['favorites']);
      qc.setQueryData(['favorites'], {
        data: ids.map((id, i) => ({ channel_id: id, position: i })),
      });
      return { previous };
    },
    onError: (err, _ids, ctx) => {
      if (ctx?.previous) qc.setQueryData(['favorites'], ctx.previous);
      toast.error(`Couldn't save order: ${(err as Error).message}`);
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['favorites'] });
    },
  });

  const sensors = useSensors(
    // Activation distance keeps the click-to-watch link working: a small
    // mouse movement is treated as a click, not a drag start.
    useSensor(PointerSensor, { activationConstraint: { distance: 6 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  );

  const handleDragEnd = (event: DragEndEvent) => {
    const { active, over } = event;
    if (!over || active.id === over.id) return;
    const oldIndex = orderedIds.indexOf(String(active.id));
    const newIndex = orderedIds.indexOf(String(over.id));
    if (oldIndex < 0 || newIndex < 0) return;
    const next = arrayMove(orderedIds, oldIndex, newIndex);
    setOrderedIds(next);
    reorderMutation.mutate(next);
  };

  if (favoritesQuery.isPending) {
    return <div className="p-6 text-sm text-[color:var(--color-muted-foreground)]">Loading favorites…</div>;
  }
  if (favoritesQuery.isError) {
    return (
      <div className="rounded-md border border-red-900/40 bg-red-950/30 p-4 text-sm text-red-300">
        Could not load favorites: {(favoritesQuery.error as Error).message}
      </div>
    );
  }

  if (orderedIds.length === 0) {
    return (
      <div className="rounded-lg border border-dashed border-[color:var(--color-border)] p-12 text-center">
        <Star className="mx-auto mb-2 text-[color:var(--color-muted-foreground)]" />
        <p className="text-sm text-[color:var(--color-muted-foreground)]">
          You haven&apos;t starred any channels yet. Open <Link className="underline" to="/channels">Channels</Link> and tap the star on the cards you want here.
        </p>
      </div>
    );
  }

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between">
        <h1 className="text-lg font-semibold tracking-tight">Favorites</h1>
        <p className="text-xs text-[color:var(--color-muted-foreground)]">
          Drag the handle to reorder. Order syncs automatically.
        </p>
      </div>

      <DndContext sensors={sensors} collisionDetection={closestCenter} onDragEnd={handleDragEnd}>
        <SortableContext items={orderedIds} strategy={rectSortingStrategy}>
          <div className="grid grid-cols-1 gap-2 sm:grid-cols-2 lg:grid-cols-3">
            {orderedIds.map((id) => (
              <SortableFavoriteRow key={id} id={id} channel={channelsById.get(id)} />
            ))}
          </div>
        </SortableContext>
      </DndContext>
    </div>
  );
}

function SortableFavoriteRow({ id, channel }: { id: string; channel?: Channel }) {
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({ id });
  const style = {
    transform: CSS.Transform.toString(transform),
    transition,
    opacity: isDragging ? 0.6 : 1,
  };

  return (
    <div
      ref={setNodeRef}
      style={style}
      className="flex items-center gap-2 rounded-lg border border-[color:var(--color-border)] bg-[color:var(--color-surface)] p-2"
    >
      <button
        type="button"
        className="cursor-grab touch-none rounded p-1 text-[color:var(--color-muted-foreground)] hover:bg-black/30 active:cursor-grabbing"
        aria-label="Drag to reorder"
        {...attributes}
        {...listeners}
      >
        <GripVertical size={16} />
      </button>
      <Link
        to={channel ? `/watch/${encodeURIComponent(channel.id)}` : '#'}
        className="flex flex-1 items-center gap-2 overflow-hidden"
      >
        <div className="flex h-10 w-10 shrink-0 items-center justify-center overflow-hidden rounded bg-black/40">
          {channel?.logo_url ? (
            <img src={channel.logo_url} alt="" className="max-h-full max-w-full object-contain" />
          ) : (
            <span className="text-[9px] text-[color:var(--color-muted-foreground)]">—</span>
          )}
        </div>
        <div className="min-w-0">
          <div className="truncate text-sm font-medium">
            {channel?.display_name ?? id}
          </div>
          {channel?.current_program ? (
            <div className="truncate text-xs text-[color:var(--color-muted-foreground)]">
              {channel.current_program.title}
            </div>
          ) : null}
        </div>
      </Link>
    </div>
  );
}
