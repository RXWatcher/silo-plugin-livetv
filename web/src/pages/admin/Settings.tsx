import { useEffect, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { toast } from 'sonner';
import { adminApi, type AdminSettings } from '@/api/admin';
import { isLikelyDuration } from '@/lib/utils';
import { Field } from './Sources';

// Settings admin page: a single form with the six global settings the
// runtime cares about (refresh defaults, guide window cap, three stream
// caps). Durations round-trip as Go duration strings; ints are positive.
// On save we PUT the whole object and surface server-side 400s as toasts.
export function Settings() {
  const qc = useQueryClient();

  const settingsQuery = useQuery({
    queryKey: ['admin', 'settings'],
    queryFn: () => adminApi.settings.get(),
  });

  const [draft, setDraft] = useState<AdminSettings | null>(null);

  useEffect(() => {
    if (settingsQuery.data) setDraft(settingsQuery.data);
  }, [settingsQuery.data]);

  const saveMutation = useMutation({
    mutationFn: (values: AdminSettings) => adminApi.settings.put(values),
    onSuccess: (saved) => {
      toast.success('Settings saved');
      qc.setQueryData(['admin', 'settings'], saved);
    },
    onError: (err: Error) => toast.error(err.message),
  });

  if (settingsQuery.isPending || !draft) {
    return <div className="py-10 text-center text-sm text-[color:var(--color-muted-foreground)]">Loading settings…</div>;
  }
  if (settingsQuery.isError) {
    return (
      <div className="rounded-md border border-red-900/40 bg-red-950/30 p-4 text-sm text-red-300">
        Could not load settings: {(settingsQuery.error as Error).message}
      </div>
    );
  }

  const intErrors = {
    user: draft.per_user_stream_cap > 0 && Number.isInteger(draft.per_user_stream_cap) ? null : 'Must be a positive integer',
    channel: draft.per_channel_default_cap > 0 && Number.isInteger(draft.per_channel_default_cap) ? null : 'Must be a positive integer',
  };
  const durationErrors = {
    m3u: isLikelyDuration(draft.default_m3u_refresh) ? null : 'Not a recognisable duration',
    xmltv: isLikelyDuration(draft.default_xmltv_refresh) ? null : 'Not a recognisable duration',
    guide: isLikelyDuration(draft.guide_window_cap) ? null : 'Not a recognisable duration',
    idle: isLikelyDuration(draft.session_idle_timeout) ? null : 'Not a recognisable duration',
  };
  const formInvalid =
    intErrors.user != null ||
    intErrors.channel != null ||
    durationErrors.m3u != null ||
    durationErrors.xmltv != null ||
    durationErrors.guide != null ||
    durationErrors.idle != null;

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    if (!draft) return;
    saveMutation.mutate(draft);
  };

  return (
    <form onSubmit={submit} className="space-y-6">
      <header>
        <h1 className="text-lg font-semibold tracking-tight">Settings</h1>
        <p className="text-xs text-[color:var(--color-muted-foreground)]">
          Plugin-wide defaults. Stored as a singleton row; updates take effect immediately.
        </p>
      </header>

      <FieldGroup
        title="Refresh defaults"
        description="Applied when new M3U/XMLTV sources don't override the interval."
      >
        <Field
          label="Default M3U refresh"
          hint='Go duration string. e.g. "6h", "3h30m".'
          error={durationErrors.m3u}
        >
          <input
            value={draft.default_m3u_refresh}
            onChange={(e) => setDraft({ ...draft, default_m3u_refresh: e.target.value })}
            className="form-input font-mono"
            required
          />
        </Field>
        <Field
          label="Default XMLTV refresh"
          hint='Go duration string. e.g. "12h", "1h".'
          error={durationErrors.xmltv}
        >
          <input
            value={draft.default_xmltv_refresh}
            onChange={(e) => setDraft({ ...draft, default_xmltv_refresh: e.target.value })}
            className="form-input font-mono"
            required
          />
        </Field>
      </FieldGroup>

      <FieldGroup
        title="Guide"
        description="Caps how far ahead the user-facing guide can render in one window."
      >
        <Field
          label="Guide window cap"
          hint='Go duration. The user UI never requests a window wider than this.'
          error={durationErrors.guide}
        >
          <input
            value={draft.guide_window_cap}
            onChange={(e) => setDraft({ ...draft, guide_window_cap: e.target.value })}
            className="form-input font-mono"
            required
          />
        </Field>
      </FieldGroup>

      <FieldGroup
        title="Stream caps"
        description="Concurrency limits and idle reaper threshold for active sessions."
      >
        <Field
          label="Per-user concurrent stream cap"
          hint="Max simultaneous sessions per user across all channels."
          error={intErrors.user}
        >
          <input
            type="number"
            min={1}
            step={1}
            value={draft.per_user_stream_cap}
            onChange={(e) => setDraft({ ...draft, per_user_stream_cap: Number(e.target.value) || 0 })}
            className="form-input"
            required
          />
        </Field>
        <Field
          label="Per-channel concurrent stream cap"
          hint="Default ceiling on viewers of the same channel. Channels can override individually."
          error={intErrors.channel}
        >
          <input
            type="number"
            min={1}
            step={1}
            value={draft.per_channel_default_cap}
            onChange={(e) => setDraft({ ...draft, per_channel_default_cap: Number(e.target.value) || 0 })}
            className="form-input"
            required
          />
        </Field>
        <Field
          label="Session idle timeout"
          hint='Go duration. A session with no recent bytes for this long is reaped.'
          error={durationErrors.idle}
        >
          <input
            value={draft.session_idle_timeout}
            onChange={(e) => setDraft({ ...draft, session_idle_timeout: e.target.value })}
            className="form-input font-mono"
            required
          />
        </Field>
      </FieldGroup>

      <div className="flex items-center justify-end gap-2 border-t border-[color:var(--color-border)] pt-4">
        <button
          type="button"
          onClick={() => {
            if (settingsQuery.data) setDraft(settingsQuery.data);
          }}
          disabled={saveMutation.isPending}
          className="rounded-md border border-[color:var(--color-border)] px-3 py-1.5 text-sm hover:bg-[color:var(--color-surface)]"
        >
          Discard
        </button>
        <button
          type="submit"
          disabled={saveMutation.isPending || formInvalid}
          className="rounded-md bg-amber-500 px-3 py-1.5 text-sm font-semibold text-black hover:bg-amber-400 disabled:opacity-60"
        >
          {saveMutation.isPending ? 'Saving…' : 'Save settings'}
        </button>
      </div>
    </form>
  );
}

function FieldGroup({
  title,
  description,
  children,
}: {
  title: string;
  description?: string;
  children: React.ReactNode;
}) {
  return (
    <section className="space-y-3 rounded-lg border border-[color:var(--color-border)] bg-[color:var(--color-surface)] p-4">
      <div>
        <h2 className="text-sm font-semibold uppercase tracking-wider text-[color:var(--color-muted-foreground)]">
          {title}
        </h2>
        {description ? (
          <p className="mt-0.5 text-xs text-[color:var(--color-muted-foreground)]/80">{description}</p>
        ) : null}
      </div>
      <div className="grid gap-3 sm:grid-cols-2">
        {children}
      </div>
    </section>
  );
}
