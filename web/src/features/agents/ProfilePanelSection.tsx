import type { ReactNode } from "react";
import { ChevronRight } from "lucide-react";
import { Collapsible, CollapsiblePanel, CollapsibleTrigger } from "@/components/ui/collapsible";

/**
 * One section of a profile tab. The profile's tabs used to invent a heading, a
 * container and a spacing rhythm each, so the same page read as four products;
 * this is the single grammar all of them now compose: a heading row (title,
 * optional count, right-side action slot) over the section's own content.
 *
 * Deliberately not a card — the tabs stack many sections and a border per
 * section would out-shout the rows inside it.
 *
 * `collapsible` is for tabs whose sections are tall enough that a plain stack
 * would bury the last one (the configuration tab's prompt editor, the memory
 * tab's lists): the heading becomes a disclosure row, matching
 * `MemorySection`'s idiom so both tabs fold the same way.
 */
export function ProfilePanelSection({
  title,
  count,
  action,
  description,
  collapsible,
  defaultOpen = false,
  children,
}: {
  title: ReactNode;
  count?: number;
  action?: ReactNode;
  description?: ReactNode;
  collapsible?: boolean;
  defaultOpen?: boolean;
  children?: ReactNode;
}) {
  const heading = (
    <h3 className="flex min-w-0 items-center gap-2 text-sm font-semibold">
      <span className="truncate">{title}</span>
      {count != null && (
        <span className="shrink-0 text-xs font-normal text-muted-foreground">{count}</span>
      )}
    </h3>
  );

  if (collapsible) {
    return (
      <Collapsible defaultOpen={defaultOpen} render={<section className="flex flex-col" />}>
        <div className="flex min-h-8 items-center justify-between gap-2 border-b border-border py-2">
          <CollapsibleTrigger className="group flex min-w-0 flex-1 cursor-pointer items-center gap-2 text-left">
            <ChevronRight className="size-3.5 shrink-0 text-muted-foreground transition-transform duration-150 ease-out group-data-[panel-open]:rotate-90" />
            {heading}
          </CollapsibleTrigger>
          {action && <div className="flex shrink-0 items-center gap-1">{action}</div>}
        </div>
        <CollapsiblePanel>
          <div className="flex flex-col gap-2 py-4">
            {description && <p className="text-xs text-muted-foreground">{description}</p>}
            {children}
          </div>
        </CollapsiblePanel>
      </Collapsible>
    );
  }

  return (
    <section className="flex flex-col gap-2">
      <div className="flex min-h-8 items-center justify-between gap-2">
        {heading}
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
