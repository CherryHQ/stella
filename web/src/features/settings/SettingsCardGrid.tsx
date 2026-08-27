import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { Card } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Sheet, SheetPopup } from "@/components/ui/sheet";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/menu";
import { MoreHorizontal } from "lucide-react";
import type { JsonObject } from "@/lib/types";

// SettingsGridPage is the full-width, scrollable shell shared by the card-grid
// settings pages: a title row with an optional action, then stacked sections.
export function SettingsGridPage({
  title,
  action,
  children,
}: {
  title: string;
  action?: ReactNode;
  children: ReactNode;
}) {
  return (
    <div className="h-full min-h-0 overflow-y-auto">
      <div className="mx-auto max-w-5xl space-y-8 p-6">
        <div className="flex items-center justify-between gap-4">
          <h1 className="text-lg font-semibold tracking-tight">{title}</h1>
          {action}
        </div>
        {children}
      </div>
    </div>
  );
}

interface SectionProps {
  icon?: ReactNode;
  title: string;
  description?: string;
  count?: number;
  action?: ReactNode;
  children: ReactNode;
}

// SettingsSection is a labelled block with a header (icon, title, count,
// description, optional trailing action) and arbitrary content — for groups that
// aren't a uniform card grid (tables, custom cards, nested panels).
export function SettingsSection({
  icon,
  title,
  description,
  count,
  action,
  children,
}: SectionProps) {
  return (
    <section className="space-y-3">
      <div className="flex items-center gap-2">
        {icon && <span className="text-muted-foreground">{icon}</span>}
        <h2 className="text-sm font-semibold text-foreground">{title}</h2>
        {count !== undefined && (
          <Badge variant="secondary" size="sm">
            {count}
          </Badge>
        )}
        {description && <span className="text-xs text-muted-foreground">— {description}</span>}
        {action && <span className="ml-auto">{action}</span>}
      </div>
      {children}
    </section>
  );
}

// SettingsCardSection is a SettingsSection whose content is a responsive card
// grid. Callers skip rendering it when a group is empty.
export function SettingsCardSection(props: SectionProps) {
  const { children, ...section } = props;
  return (
    <SettingsSection {...section}>
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">{children}</div>
    </SettingsSection>
  );
}

// SettingsCard is the generic catalog card: an icon, a title (with optional
// badge), a clamped description, a top-right action (e.g. a toggle), and an
// optional footer row. Clicks on the action slot don't trigger onClick.
export function SettingsCard({
  icon,
  title,
  badge,
  description,
  action,
  footer,
  active,
  onClick,
  children,
  to,
  params,
  search,
}: {
  icon?: ReactNode;
  title: ReactNode;
  badge?: ReactNode;
  description?: ReactNode;
  action?: ReactNode;
  footer?: ReactNode;
  active?: boolean;
  onClick?: () => void;
  children?: ReactNode;
  to?: string;
  params?: Record<string, string>;
  search?: JsonObject;
}) {
  // SAFETY: params/search map to the selected settings route when present; coerced to Link's typed unions.
  const linkParams = params as never;
  // SAFETY: same coercion for the route's search string-map.
  const linkSearch = search as never;
  const linkElement = to ? <Link to={to} params={linkParams} search={linkSearch} /> : undefined;
  return (
    <Card
      render={linkElement}
      onClick={onClick}
      className={`gap-3 p-4 transition-colors ${
        onClick || to ? "cursor-pointer hover:border-ring/40" : ""
      } ${active ? "border-ring/60" : ""}`}
    >
      <div className="flex items-start gap-3">
        {icon && (
          <span className="grid size-9 shrink-0 place-items-center rounded-lg border border-border bg-muted text-muted-foreground">
            {icon}
          </span>
        )}
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-1.5">
            <span className="truncate text-sm font-medium text-foreground">{title}</span>
            {badge}
          </div>
          {description && (
            <p className="mt-0.5 line-clamp-2 text-xs text-muted-foreground">{description}</p>
          )}
        </div>
        {action && (
          <span
            className="shrink-0"
            onClick={(e) => {
              e.preventDefault();
              e.stopPropagation();
            }}
            onKeyDown={(e) => e.stopPropagation()}
            role="presentation"
          >
            {action}
          </span>
        )}
      </div>
      {children}
      {footer && (
        <div className="flex items-center gap-1.5 border-t border-border pt-2.5">{footer}</div>
      )}
    </Card>
  );
}

// SettingsList is a bordered container of divided rows — the dense, uniform
// alternative to a card grid for connection/account/secret lists.
export function SettingsList({ children }: { children: ReactNode }) {
  return (
    <div className="divide-y divide-border overflow-hidden rounded-xl border border-border">
      {children}
    </div>
  );
}

export interface RowAction {
  label: string;
  onClick: () => void;
  destructive?: boolean;
  disabled?: boolean;
}

// SettingsRow is one compact list row: icon, title (+ chip), a one-line subtitle,
// a status pill, an optional primary action, and an overflow menu for the rest.
// The action cluster wraps below the title on narrow screens (mobile).
export function SettingsRow({
  icon,
  title,
  chip,
  subtitle,
  status,
  primary,
  menu,
  onClick,
}: {
  icon?: ReactNode;
  title: ReactNode;
  chip?: ReactNode;
  subtitle?: ReactNode;
  status?: ReactNode;
  primary?: ReactNode;
  menu?: RowAction[];
  onClick?: () => void;
}) {
  return (
    <div
      onClick={onClick}
      className={`flex flex-wrap items-center gap-x-3 gap-y-2 px-4 py-3 ${
        onClick ? "cursor-pointer hover:bg-muted/40" : ""
      }`}
    >
      {icon && (
        <span className="grid size-8 shrink-0 place-items-center rounded-lg border border-border bg-muted text-muted-foreground">
          {icon}
        </span>
      )}
      <div className="min-w-0 flex-1 basis-44">
        <div className="flex items-center gap-1.5">
          <span className="truncate text-sm font-medium text-foreground">{title}</span>
          {chip}
        </div>
        {subtitle && <p className="truncate text-xs text-muted-foreground">{subtitle}</p>}
      </div>
      <div
        className="ml-auto flex items-center gap-2"
        onClick={(e) => e.stopPropagation()}
        onKeyDown={(e) => e.stopPropagation()}
        role="presentation"
      >
        {status}
        {primary}
        {menu && menu.length > 0 && (
          <DropdownMenu>
            <DropdownMenuTrigger
              aria-label="More actions"
              className="grid size-7 shrink-0 cursor-pointer place-items-center rounded-md text-muted-foreground outline-none hover:bg-muted hover:text-foreground"
            >
              <MoreHorizontal className="size-4" />
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              {menu.map((action) => (
                <DropdownMenuItem
                  key={action.label}
                  disabled={action.disabled}
                  variant={action.destructive ? "destructive" : "default"}
                  onClick={action.onClick}
                >
                  {action.label}
                </DropdownMenuItem>
              ))}
            </DropdownMenuContent>
          </DropdownMenu>
        )}
      </div>
    </div>
  );
}

// SettingsDetailSheet is the controlled right-side overlay that holds a page's
// detail/config content. Closing (backdrop, Esc, X) calls onClose.
export function SettingsDetailSheet({
  open,
  onClose,
  children,
}: {
  open: boolean;
  onClose: () => void;
  children: ReactNode;
}) {
  return (
    <Sheet open={open} onOpenChange={(next) => !next && onClose()}>
      <SheetPopup side="right" className="h-full w-full p-0 sm:max-w-2xl">
        {children}
      </SheetPopup>
    </Sheet>
  );
}
