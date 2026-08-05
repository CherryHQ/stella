import type { ReactNode } from "react";

/**
 * One section of a profile tab. The profile's tabs used to invent a heading, a
 * container and a spacing rhythm each, so the same page read as four products;
 * this is the single grammar all of them now compose: a heading row (title,
 * optional count, right-side action slot) over the section's own content.
 *
 * Deliberately not a card — the tabs stack many sections and a border per
 * section would out-shout the rows inside it.
 */
export function ProfilePanelSection({
  title,
  count,
  action,
  description,
  children,
}: {
  title: ReactNode;
  count?: number;
  action?: ReactNode;
  description?: ReactNode;
  children?: ReactNode;
}) {
  return (
    <section className="flex flex-col gap-2">
      <div className="flex min-h-8 items-center justify-between gap-2">
        <h3 className="flex min-w-0 items-center gap-2 text-sm font-semibold">
          <span className="truncate">{title}</span>
          {count != null && (
            <span className="shrink-0 text-xs font-normal text-muted-foreground">{count}</span>
          )}
        </h3>
        {action && <div className="flex shrink-0 items-center gap-1">{action}</div>}
      </div>
      {description && <p className="text-xs text-muted-foreground">{description}</p>}
      {children}
    </section>
  );
}

/**
 * The one shape every loading, empty and error line in the profile takes, so a
 * tab never has to decide between a spinner, an italic line and a bare string.
 */
export function ProfileSectionMessage({ children }: { children: ReactNode }) {
  return <p className="py-6 text-center text-sm text-muted-foreground">{children}</p>;
}
