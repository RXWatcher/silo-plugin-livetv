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
