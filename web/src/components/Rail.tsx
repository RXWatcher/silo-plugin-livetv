import { type ReactNode } from 'react';

interface Props {
  title: string;
  children: ReactNode;
  empty?: ReactNode;
}

// Rail is a horizontally scrollable strip used by the Home page. It
// snaps its children onto a row with a thin, hover-revealed scrollbar
// (see .guide-scroll in index.css for the same pattern). Children are
// laid out as flex items; callers control width per card.
export function Rail({ title, children, empty }: Props) {
  return (
    <section className="space-y-2">
      <h2 className="text-sm font-semibold uppercase tracking-wider text-[color:var(--color-muted-foreground)]">
        {title}
      </h2>
      <div className="guide-scroll -mx-3 overflow-x-auto px-3 md:-mx-6 md:px-6">
        <div className="flex gap-3 pb-2">
          {children}
          {empty}
        </div>
      </div>
    </section>
  );
}
