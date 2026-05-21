import { clsx, type ClassValue } from 'clsx';
import { twMerge } from 'tailwind-merge';

export function cn(...inputs: ClassValue[]): string {
  return twMerge(clsx(inputs));
}

// formatTime renders an ISO timestamp as "HH:MM" in the user's local TZ.
export function formatTime(iso: string): string {
  const d = new Date(iso);
  return d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' });
}

// formatTimeRange renders "HH:MM - HH:MM" for a program window.
export function formatTimeRange(startIso: string, stopIso: string): string {
  return `${formatTime(startIso)} – ${formatTime(stopIso)}`;
}

// isAiringNow returns true when start <= now < stop.
export function isAiringNow(startIso: string, stopIso: string, now: Date = new Date()): boolean {
  const t = now.getTime();
  return new Date(startIso).getTime() <= t && t < new Date(stopIso).getTime();
}

// formatRelative renders an ISO timestamp as "Ns/Nm/Nh/Nd ago" (or "in N…"
// for future times). Used by admin pages to show "last refreshed 3m ago"
// without pulling in date-fns just for one formatter.
export function formatRelative(iso: string | undefined | null, now: Date = new Date()): string {
  if (!iso) return '—';
  const then = new Date(iso).getTime();
  if (Number.isNaN(then)) return '—';
  const delta = Math.round((now.getTime() - then) / 1000);
  const abs = Math.abs(delta);
  const suffix = delta >= 0 ? 'ago' : 'from now';
  if (abs < 5) return delta >= 0 ? 'just now' : 'in a moment';
  if (abs < 60) return `${abs}s ${suffix}`;
  if (abs < 3600) return `${Math.floor(abs / 60)}m ${suffix}`;
  if (abs < 86_400) return `${Math.floor(abs / 3600)}h ${suffix}`;
  return `${Math.floor(abs / 86_400)}d ${suffix}`;
}

// formatBytes renders a byte count as a humanised IEC-prefixed string. We
// pick IEC (KiB/MiB/GiB) because admin tooling tends to report exact byte
// counters and rounding to SI would be confusing here.
export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 B';
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB'];
  let n = bytes;
  let i = 0;
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024;
    i += 1;
  }
  // One decimal for KiB+, integer for B.
  return i === 0 ? `${Math.round(n)} ${units[i]}` : `${n.toFixed(1)} ${units[i]}`;
}

// isLikelyDuration is a forgiving client-side check for Go duration strings.
// We accept the common units the API surfaces (ns, us, µs, ms, s, m, h) and
// trust the server's parser to reject anything more exotic. Used by the
// admin Settings + Sources forms to avoid round-tripping obvious typos.
const durationRe = /^(\d+(?:\.\d+)?(?:ns|us|µs|ms|s|m|h))+$/;
export function isLikelyDuration(s: string): boolean {
  return durationRe.test(s.trim());
}
