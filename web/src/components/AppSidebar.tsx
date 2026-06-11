import { Link, useRouterState } from "@tanstack/react-router";
import { useI18n } from "@/lib/i18n";
import { cn } from "@/lib/utils";
import { ThemeSelector, UserMenu } from "@/components/SiteHeader";
import { ChevronDown, Bot, BookOpen, Settings } from "lucide-react";
import type { ReactNode } from "react";
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuGroup,
} from "@/components/ui/menu";

export function AppSidebarHeader() {
  const { t } = useI18n();
  const routePathname = useRouterState({ select: (s) => s.location.pathname });

  const currentContext = routePathname.startsWith("/agents")
    ? "agents"
    : routePathname.startsWith("/recally")
      ? "recally"
      : routePathname.startsWith("/settings")
        ? "settings"
        : "agents";

  const labelKey =
    currentContext === "agents"
      ? "nav.sessions"
      : currentContext === "recally"
        ? "nav.recally"
        : "nav.settings";

  return (
    <div className="flex h-12 shrink-0 items-center border-b border-border bg-card/70 px-3 backdrop-blur-xl">
      <DropdownMenu>
        <DropdownMenuTrigger className="flex w-full items-center justify-between rounded-lg border border-border/30 bg-muted/20 px-2.5 py-1.5 text-left transition-colors hover:bg-muted/40 outline-none select-none cursor-pointer">
          <div className="flex items-center gap-2 min-w-0">
            <img
              src="/stella-monogram.svg"
              alt=""
              width={18}
              height={18}
              className="rounded-xs shrink-0"
            />
            <span className="font-serif text-sm italic tracking-tight text-foreground select-none shrink-0">
              stella
            </span>
            <span className="text-muted-foreground/30 text-xs font-normal">/</span>
            <span className="text-xs font-semibold text-foreground truncate">
              {t(labelKey as any)}
            </span>
          </div>
          <ChevronDown className="size-3.5 shrink-0 text-muted-foreground/70 ml-1" />
        </DropdownMenuTrigger>
        <DropdownMenuContent align="start" className="w-56" sideOffset={6}>
          <DropdownMenuGroup>
            <DropdownMenuLabel className="text-xs font-mono tracking-wider text-muted-foreground/60 uppercase px-2 py-1">
              {t("common.context")}
            </DropdownMenuLabel>
            <DropdownMenuItem
              render={<Link to="/agents" />}
              className={cn(
                "gap-2.5 py-2 text-xs font-medium cursor-pointer rounded-md",
                currentContext === "agents" && "bg-accent text-primary",
              )}
            >
              <div
                className={cn(
                  "flex size-6 items-center justify-center rounded-md border border-border/30 bg-muted/40",
                  currentContext === "agents" && "border-primary/20 bg-primary/5 text-primary",
                )}
              >
                <Bot className="size-3.5" />
              </div>
              <div className="flex flex-col min-w-0">
                <span className="font-medium text-foreground">{t("nav.sessions")}</span>
                <span className="text-xs text-muted-foreground font-normal truncate">
                  {t("nav.sessions.desc" as any) || "AI chat assistant & projects"}
                </span>
              </div>
            </DropdownMenuItem>
            <DropdownMenuItem
              render={<Link to="/recally" />}
              className={cn(
                "gap-2.5 py-2 text-xs font-medium cursor-pointer rounded-md",
                currentContext === "recally" && "bg-accent text-primary",
              )}
            >
              <div
                className={cn(
                  "flex size-6 items-center justify-center rounded-md border border-border/30 bg-muted/40",
                  currentContext === "recally" && "border-primary/20 bg-primary/5 text-primary",
                )}
              >
                <BookOpen className="size-3.5" />
              </div>
              <div className="flex flex-col min-w-0">
                <span className="font-medium text-foreground">{t("nav.recally")}</span>
                <span className="text-xs text-muted-foreground font-normal truncate">
                  {t("nav.recally.desc" as any) || "Read queue, feeds & memory"}
                </span>
              </div>
            </DropdownMenuItem>
            <DropdownMenuItem
              render={<Link to="/settings" />}
              className={cn(
                "gap-2.5 py-2 text-xs font-medium cursor-pointer rounded-md",
                currentContext === "settings" && "bg-accent text-primary",
              )}
            >
              <div
                className={cn(
                  "flex size-6 items-center justify-center rounded-md border border-border/30 bg-muted/40",
                  currentContext === "settings" && "border-primary/20 bg-primary/5 text-primary",
                )}
              >
                <Settings className="size-3.5" />
              </div>
              <div className="flex flex-col min-w-0">
                <span className="font-medium text-foreground">{t("nav.settings")}</span>
                <span className="text-xs text-muted-foreground font-normal truncate">
                  {t("settings.title") || "Settings"}
                </span>
              </div>
            </DropdownMenuItem>
          </DropdownMenuGroup>
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  );
}

export function AppSidebarFooter() {
  const { t } = useI18n();
  const routePathname = useRouterState({ select: (s) => s.location.pathname });

  return (
    <div className="shrink-0 border-t border-border/60 bg-card/60 px-3 py-1.5 backdrop-blur-xl">
      <div className="flex items-center gap-1.5">
        <Link
          to="/settings"
          className={cn(
            "flex h-7 min-w-0 flex-1 items-center gap-2 rounded-xl px-2.5 text-left text-xs font-medium tracking-[-0.01em] transition-colors cursor-pointer",
            routePathname.startsWith("/settings")
              ? "bg-accent text-primary"
              : "text-muted-foreground hover:bg-foreground/[0.045] hover:text-foreground",
          )}
        >
          <Settings
            className={cn(
              "size-3.5 shrink-0",
              routePathname.startsWith("/settings") ? "text-primary" : "text-muted-foreground/70",
            )}
          />
          <span className="truncate">{t("nav.settings")}</span>
        </Link>
        <ThemeSelector />
        <UserMenu />
      </div>
    </div>
  );
}

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
  children,
  open = true,
  onOpenChange,
  action,
  className,
}: {
  title: ReactNode;
  children: ReactNode;
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
  action?: ReactNode;
  className?: string;
}) {
  const headerClassName =
    "flex h-full min-w-0 flex-1 items-center gap-2 rounded-lg px-2 font-mono text-xs text-muted-foreground";
  const interactiveHeaderClassName = cn(
    headerClassName,
    "cursor-pointer hover:bg-foreground/[0.045] hover:text-muted-foreground",
  );

  return (
    <section className={cn("mt-3", className)}>
      <div className="flex h-[30px] items-center gap-1 pr-1">
        {onOpenChange ? (
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
          </button>
        ) : (
          <div className={headerClassName}>
            <span className="truncate">{title}</span>
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
