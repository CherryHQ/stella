import { useNavigate, useRouterState } from "@tanstack/react-router";
import { useI18n } from "@/lib/i18n";
import { cn } from "@/lib/utils";
import { ThemeSelector, UserMenu } from "@/components/SiteHeader";
import { ChevronDown, Bot, BookOpen, Settings } from "lucide-react";
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuGroup,
} from "@/components/ui/menu";

interface SidebarContainerProps {
  children?: React.ReactNode;
  className?: string;
}

export function SidebarContainer({ children, className }: SidebarContainerProps) {
  return (
    <aside
      className={cn(
        "flex h-full w-[260px] shrink-0 flex-col overflow-hidden border-r border-border bg-card/40",
        className,
      )}
    >
      {children}
    </aside>
  );
}

export function SidebarHeader() {
  const navigate = useNavigate();
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
    <div className="shrink-0 border-b border-border bg-card/70 px-3 py-2.5 backdrop-blur-xl">
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
            <DropdownMenuLabel className="text-[10px] font-mono tracking-wider text-muted-foreground/60 uppercase px-2 py-1">
              {t("common.context")}
            </DropdownMenuLabel>
            <DropdownMenuItem
              onClick={() => void navigate({ to: "/agents" })}
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
                <span className="text-[10px] text-muted-foreground font-normal truncate">
                  {t("nav.sessions.desc" as any) || "AI chat assistant & projects"}
                </span>
              </div>
            </DropdownMenuItem>
            <DropdownMenuItem
              onClick={() => void navigate({ to: "/recally" })}
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
                <span className="text-[10px] text-muted-foreground font-normal truncate">
                  {t("nav.recally.desc" as any) || "Read queue, feeds & memory"}
                </span>
              </div>
            </DropdownMenuItem>
            <DropdownMenuItem
              onClick={() => void navigate({ to: "/settings" })}
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
                <span className="text-[10px] text-muted-foreground font-normal truncate">
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

export function SidebarFooter() {
  const navigate = useNavigate();
  const { t } = useI18n();
  const routePathname = useRouterState({ select: (s) => s.location.pathname });

  return (
    <div className="shrink-0 border-t border-border/60 bg-card/60 px-3 py-1.5 backdrop-blur-xl">
      <div className="flex items-center gap-1.5">
        <button
          type="button"
          onClick={() => void navigate({ to: "/settings" })}
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
        </button>
        <ThemeSelector />
        <UserMenu />
      </div>
    </div>
  );
}

export function SectionLabel({ children }: { children: React.ReactNode }) {
  return (
    <div className="px-2 pb-1.5 pt-4 font-mono text-[10px] uppercase tracking-[0.08em] text-muted-foreground/60">
      {children}
    </div>
  );
}
