import { Link } from "@tanstack/react-router";
import { cn } from "@/lib/utils";
import type { ReactNode } from "react";

function SidebarChevron({ className }: { className?: string }) {
  return (
    <svg
      className={className}
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
    >
      <path d="m6 4 4 4-4 4" />
    </svg>
  );
}

export function SidebarSection({
  title,
  titleLink,
  children,
  open = true,
  onOpenChange,
  count,
  action,
  className,
}: {
  title: ReactNode;
  /**
   * Renders the label as a destination instead of plain text — for a section
   * that *is* a space (its own page) rather than just a grouping. The caller
   * supplies the whole element so routing stays type-safe here; the collapse
   * moves onto the chevron, because a link inside a button is invalid markup.
   */
  titleLink?: ReactNode;
  children: ReactNode;
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
  /**
   * How many rows are hidden behind a collapsed label. Shown only while
   * collapsed — once the rows are visible the number is noise.
   */
  count?: number;
  action?: ReactNode;
  className?: string;
}) {
  // px-2.5 matches SidebarItem so a section label lines up with the rows under it.
  const headerClassName =
    "flex h-full min-w-0 flex-1 items-center gap-2 rounded-lg px-2.5 font-mono text-xs text-muted-foreground";
  const interactiveHeaderClassName = cn(
    headerClassName,
    "cursor-pointer hover:bg-foreground/[0.045] hover:text-muted-foreground",
  );

  const hiddenCount = !open && count ? <span className="ml-auto shrink-0">{count}</span> : null;

  return (
    <section className={cn("mt-4", className)}>
      <div className="flex h-[30px] items-center gap-1 pr-1">
        {titleLink ? (
          <div className={headerClassName}>
            <span className="min-w-0 truncate hover:text-foreground">{titleLink}</span>
            {onOpenChange && (
              <button
                type="button"
                aria-expanded={open}
                aria-label={typeof title === "string" ? title : undefined}
                onClick={() => onOpenChange(!open)}
                className="grid size-4 shrink-0 cursor-pointer place-items-center rounded hover:text-foreground"
              >
                <SidebarChevron
                  className={cn("size-2.5 transition-transform duration-150", open && "rotate-90")}
                />
              </button>
            )}
            {hiddenCount}
          </div>
        ) : onOpenChange ? (
          <button
            type="button"
            onClick={() => onOpenChange(!open)}
            className={interactiveHeaderClassName}
          >
            <span className="truncate">{title}</span>
            <SidebarChevron
              className={cn(
                "size-2.5 text-muted-foreground transition-transform duration-150",
                open && "rotate-90",
              )}
            />
            {hiddenCount}
          </button>
        ) : (
          <div className={headerClassName}>
            <span className="truncate">{title}</span>
            {hiddenCount}
          </div>
        )}
        {action}
      </div>
      {open && <div className="grid min-w-0 gap-px overflow-hidden">{children}</div>}
    </section>
  );
}

export function SidebarItem({
  active,
  emphasized,
  icon,
  label,
  badge,
  meta,
  trailing,
  onClick,
  className,
  to,
  params,
}: {
  active?: boolean;
  /**
   * A row that owns the rows below it (an expanded accordion parent) without
   * being the current destination: emphasized text, no fill. Keeping the fill
   * exclusive to `active` means exactly one row ever reads as "you are here".
   */
  emphasized?: boolean;
  icon?: ReactNode;
  label: ReactNode;
  badge?: ReactNode;
  meta?: ReactNode;
  trailing?: ReactNode;
  onClick?: () => void;
  className?: string;
  to?: string;
  params?: Record<string, string>;
}) {
  const itemClassName = cn(
    "flex min-h-[34px] w-full min-w-0 cursor-pointer items-center gap-2.5 overflow-hidden rounded-lg px-2.5 py-1 text-left text-[13px] tracking-[-0.01em] transition-all duration-150 border",
    active
      ? "bg-muted font-semibold text-foreground border-border/60"
      : emphasized
        ? "font-semibold text-foreground hover:bg-muted/40 border-transparent"
        : "text-muted-foreground hover:bg-muted/40 hover:text-foreground border-transparent",
    className,
  );
  const content = (
    <>
      {icon && <span className="grid size-6 shrink-0 place-items-center">{icon}</span>}
      <span className="min-w-0 flex-1 truncate">{label}</span>
      {badge}
      {meta && <span className="shrink-0 text-muted-foreground">{meta}</span>}
      {trailing}
    </>
  );

  if (to) {
    return (
      <Link to={to} params={params as never} onClick={onClick} className={itemClassName}>
        {content}
      </Link>
    );
  }

  return (
    <button type="button" onClick={onClick} className={itemClassName}>
      {content}
    </button>
  );
}
