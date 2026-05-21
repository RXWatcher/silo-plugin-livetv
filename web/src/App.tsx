import { Route, Routes, NavLink, useLocation, type Location } from 'react-router';
import { Home } from '@/pages/Home';
import { Channels } from '@/pages/Channels';
import { Guide } from '@/pages/Guide';
import { Favorites } from '@/pages/Favorites';
import { Search } from '@/pages/Search';
import { PlayerPage } from '@/pages/Player';
import { ProgramDetailRoute } from '@/components/ProgramDetail';
import { cn } from '@/lib/utils';

// The user portal is a single-window app with a tab bar on top. Routes are
// flat under the plugin mount — admin pages (mounted at /admin/*) come in
// Phase 9 and render under their own layout (see the placeholder route).
//
// Program detail uses react-router's "background location" pattern: when a
// link sets state.background, we render the modal as an overlay on top of
// the underlying route instead of replacing it. This keeps the guide grid
// behind the dialog without rebuilding it on close.
export function App() {
  const location = useLocation() as Location & { state?: { background?: Location } };
  const background = location.state?.background;

  return (
    <div className="min-h-screen bg-[color:var(--color-background)] text-[color:var(--color-foreground)]">
      <TopNav />
      <main className="mx-auto max-w-[1600px] px-3 py-4 md:px-6">
        <Routes location={background ?? location}>
          <Route path="/" element={<Home />} />
          <Route path="/guide" element={<Guide />} />
          <Route path="/channels" element={<Channels />} />
          <Route path="/favorites" element={<Favorites />} />
          <Route path="/search" element={<Search />} />
          <Route path="/watch/:channelId" element={<PlayerPage />} />
          <Route path="/programs/:id" element={<ProgramDetailRoute mode="page" />} />
          <Route path="/admin/*" element={<AdminPlaceholder />} />
        </Routes>

        {/* Modal route layered on top of the page underneath. Only rendered
            when a navigation explicitly carries state.background, so direct
            visits to /programs/:id still get the full-page version above. */}
        {background ? (
          <Routes>
            <Route path="/programs/:id" element={<ProgramDetailRoute mode="modal" />} />
          </Routes>
        ) : null}
      </main>
    </div>
  );
}

function TopNav() {
  const tab = (props: { to: string; label: string; end?: boolean }) => (
    <NavLink
      to={props.to}
      end={props.end}
      className={({ isActive }) =>
        cn(
          'rounded-md px-3 py-1.5 text-sm font-medium transition-colors',
          isActive
            ? 'bg-[color:var(--color-surface-hover)] text-[color:var(--color-foreground)]'
            : 'text-[color:var(--color-muted-foreground)] hover:bg-[color:var(--color-surface)]',
        )
      }
    >
      {props.label}
    </NavLink>
  );
  return (
    <nav className="sticky top-0 z-30 flex items-center gap-2 border-b border-[color:var(--color-border)] bg-[color:var(--color-background)]/85 px-3 py-2 backdrop-blur md:px-6">
      <div className="mr-3 text-sm font-semibold tracking-wide uppercase text-[color:var(--color-muted-foreground)]">
        Live TV
      </div>
      {tab({ to: '/', label: 'Home', end: true })}
      {tab({ to: '/guide', label: 'Guide' })}
      {tab({ to: '/channels', label: 'Channels' })}
      {tab({ to: '/favorites', label: 'Favorites' })}
      {tab({ to: '/search', label: 'Search' })}
    </nav>
  );
}

function AdminPlaceholder() {
  return (
    <div className="rounded-lg border border-dashed border-[color:var(--color-border)] p-8 text-center text-[color:var(--color-muted-foreground)]">
      Admin pages land in Phase 9.
    </div>
  );
}
