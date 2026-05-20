import { useEffect, useState } from "react";
import { Link, Outlet, useRouterState } from "@tanstack/react-router";
import { Menu, Search, UserRound } from "lucide-react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { meQueryOptions } from "@/lib/queries/me";
import { agentsQueryOptions } from "@/lib/queries/agents";
import { agentProjectsOptions } from "@/lib/queries/projects";
import type { Agent, Project } from "@/lib/types";
import { useI18n } from "@/lib/i18n";
import { cn } from "@/lib/utils";

const LEFT_COLLAPSED_KEY = "stella-left-collapsed";

export function AppLayout() {
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const queryClient = useQueryClient();
  const me = queryClient.getQueryData(meQueryOptions.queryKey);
  const { t } = useI18n();

  const agentId = pathname.match(/\/agents\/([^/]+)/)?.[1] ?? "";
  const projectId = pathname.match(/\/projects\/([^/]+)/)?.[1] ?? "";
  const { data: agents = [] } = useQuery(agentsQueryOptions);
  const { data: projects = [] } = useQuery({
    ...agentProjectsOptions(agentId),
    enabled: !!agentId,
  });
  const agentName = (agents as Agent[]).find((a) => a.id === agentId)?.name;
  const projectName = projectId
    ? (projects as Project[]).find((p) => p.id === projectId)?.name
    : undefined;

  const [leftCollapsed] = useStoredPanelState(LEFT_COLLAPSED_KEY, false);
  const [mobileNavOpen, setMobileNavOpen] = useState(false);

  const navItems = [
    { label: t("nav.sessions"), href: "/agents" },
    { label: t("nav.recally"), href: "/recally" },
    { label: t("nav.settings"), href: "/settings" },
    { label: t("nav.docs"), href: "/docs" },
  ];

  return (
    <div
      data-left-collapsed={leftCollapsed ? "true" : "false"}
      className="relative isolate flex h-svh flex-col overflow-hidden bg-background text-foreground"
    >
      <header className="relative z-40 flex h-[52px] shrink-0 items-center justify-between border-b border-border/70 bg-background/80 px-3 shadow-[0_1px_0_rgba(255,255,255,0.55)_inset] backdrop-blur-xl supports-[backdrop-filter]:bg-background/75 sm:px-4">
        <div className="flex min-w-0 items-center gap-2.5">
          <button
            type="button"
            className="grid size-8 place-items-center rounded-full text-muted-foreground transition-colors hover:bg-accent hover:text-foreground md:hidden"
            aria-label="Open navigation"
            onClick={() => setMobileNavOpen(true)}
          >
            <Menu className="size-4" />
          </button>
          <Link
            to="/agents"
            className="flex shrink-0 items-center gap-2.5"
            aria-label="Stella home"
          >
            <span className="grid size-6 place-items-center rounded-[7px] bg-foreground text-xs font-bold text-background shadow-sm">
              S
            </span>
            <span className="text-sm font-semibold tracking-[-0.01em]">Stella</span>
          </Link>
          <div className="hidden min-w-0 items-center gap-1.5 text-xs text-muted-foreground md:flex">
            <span>/</span>
            <span className="truncate font-medium text-foreground">{routeCrumb(pathname)}</span>
            {agentName && (
              <>
                <span>/</span>
                <span className="truncate font-medium text-foreground">{agentName}</span>
              </>
            )}
            {projectName && (
              <>
                <span>/</span>
                <span className="truncate font-medium text-foreground">{projectName}</span>
              </>
            )}
          </div>
        </div>

        <button
          type="button"
          className="hidden h-8 w-[min(420px,36vw)] items-center gap-2 rounded-full border border-border/70 bg-card/75 px-3 text-left text-xs text-muted-foreground shadow-sm transition-colors hover:bg-card md:flex"
          aria-label="Search Stella"
        >
          <Search className="size-3.5" />
          <span className="truncate">Search Stella</span>
          <kbd className="ml-auto rounded-md border border-border bg-muted px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground">
            ⌘K
          </kbd>
        </button>

        <div className="flex items-center gap-1.5">
          <Link
            to="/profile"
            className="grid size-8 place-items-center rounded-full text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
            aria-label="Account"
            title={me?.username ?? "Account"}
          >
            <UserRound className="size-4" />
          </Link>
        </div>
      </header>

      {mobileNavOpen && (
        <div className="fixed inset-x-0 top-[52px] bottom-0 z-50 md:hidden">
          <button
            type="button"
            className="absolute inset-0 bg-foreground/20 backdrop-blur-sm"
            aria-label="Close navigation"
            onClick={() => setMobileNavOpen(false)}
          />
          <nav className="absolute left-0 top-0 bottom-0 w-[min(82vw,300px)] overflow-y-auto border-r border-border bg-sidebar p-3 shadow-2xl">
            <div className="mb-3 px-2 text-[10px] font-mono uppercase tracking-[0.08em] text-muted-foreground">
              Workspace
            </div>
            <div className="grid gap-1">
              {navItems.map((item) => (
                <ShellNavLink
                  key={item.href}
                  href={item.href}
                  label={item.label}
                  pathname={pathname}
                  onClick={() => setMobileNavOpen(false)}
                />
              ))}
            </div>
          </nav>
        </div>
      )}

      <main className="min-h-0 flex-1 overflow-hidden bg-[radial-gradient(900px_420px_at_50%_-180px,color-mix(in_oklch,var(--primary)_12%,transparent),transparent_62%)]">
        <Outlet />
      </main>
    </div>
  );
}

function useStoredPanelState(key: string, fallback: boolean) {
  const [value, setValue] = useState(fallback);

  useEffect(() => {
    setValue(localStorage.getItem(key) === "1");
  }, [key]);

  useEffect(() => {
    localStorage.setItem(key, value ? "1" : "0");
  }, [key, value]);

  return [value, setValue] as const;
}

function ShellNavLink({
  href,
  label,
  pathname,
  onClick,
}: {
  href: string;
  label: string;
  pathname: string;
  onClick: () => void;
}) {
  const active = pathname === href || pathname.startsWith(href + "/");
  return (
    <Link
      to={href as never}
      onClick={onClick}
      className={cn(
        "flex h-9 items-center rounded-xl px-3 text-sm font-medium transition-colors",
        active
          ? "bg-accent text-accent-foreground"
          : "text-muted-foreground hover:bg-accent/60 hover:text-foreground",
      )}
    >
      {label}
    </Link>
  );
}

function routeCrumb(pathname: string) {
  if (pathname.startsWith("/settings")) return "Settings";
  if (pathname.startsWith("/recally")) return "Recally";
  if (pathname.startsWith("/automations")) return "Automations";
  if (pathname.startsWith("/scheduler")) return "Scheduler";
  if (pathname.startsWith("/agents")) return "Agents";
  return "Workspace";
}
