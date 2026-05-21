import { lazy, Suspense } from 'react';
import { NavLink, Navigate, Route, Routes } from 'react-router';
import { Cable, Tv, Activity, Settings2 } from 'lucide-react';
import { cn } from '@/lib/utils';

// Lazy-load each admin page so a deep-link to /admin/settings doesn't pull
// in the (potentially large) Channels table code. The admin bundle is
// already gated behind the App.tsx Suspense boundary, but splitting again
// here keeps each tab snappy on first paint.
const Sources = lazy(() => import('./Sources').then((m) => ({ default: m.Sources })));
const Channels = lazy(() => import('./Channels').then((m) => ({ default: m.Channels })));
const Sessions = lazy(() => import('./Sessions').then((m) => ({ default: m.Sessions })));
const Settings = lazy(() => import('./Settings').then((m) => ({ default: m.Settings })));

// AdminLayout is the shell for every /admin/* route. The left rail mirrors
// the top nav in the user portal but uses icons so it can collapse to a
// narrow gutter on mobile. /admin (no suffix) redirects to /admin/sources
// because that's the most common landing page on first setup.
export function AdminLayout() {
  return (
    <div className="flex min-h-[calc(100dvh-3.5rem)] flex-col gap-4 sm:flex-row">
      <aside className="shrink-0 sm:w-52">
        <nav className="flex flex-row gap-1 overflow-x-auto rounded-lg border border-[color:var(--color-border)] bg-[color:var(--color-surface)] p-1 sm:flex-col sm:overflow-visible">
          <SideLink to="sources" label="Sources" icon={<Cable size={16} />} />
          <SideLink to="channels" label="Channels" icon={<Tv size={16} />} />
          <SideLink to="sessions" label="Sessions" icon={<Activity size={16} />} />
          <SideLink to="settings" label="Settings" icon={<Settings2 size={16} />} />
        </nav>
      </aside>
      <section className="min-w-0 flex-1">
        <Suspense fallback={<div className="p-4 text-sm text-[color:var(--color-muted-foreground)]">Loading…</div>}>
          <Routes>
            <Route index element={<Navigate to="sources" replace />} />
            <Route path="sources" element={<Sources />} />
            <Route path="channels" element={<Channels />} />
            <Route path="sessions" element={<Sessions />} />
            <Route path="settings" element={<Settings />} />
            <Route path="*" element={<Navigate to="sources" replace />} />
          </Routes>
        </Suspense>
      </section>
    </div>
  );
}

function SideLink({ to, label, icon }: { to: string; label: string; icon: React.ReactNode }) {
  return (
    <NavLink
      to={to}
      className={({ isActive }) =>
        cn(
          'flex items-center gap-2 rounded-md px-3 py-2 text-sm font-medium transition-colors',
          isActive
            ? 'bg-[color:var(--color-surface-hover)] text-[color:var(--color-foreground)]'
            : 'text-[color:var(--color-muted-foreground)] hover:bg-[color:var(--color-surface-hover)]/60',
        )
      }
    >
      <span className="text-[color:var(--color-accent)]">{icon}</span>
      <span>{label}</span>
    </NavLink>
  );
}
