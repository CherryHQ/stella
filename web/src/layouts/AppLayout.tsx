import { useEffect, useState } from "react";
import { Link, Outlet, useRouterState } from "@tanstack/react-router";
import { Menu } from "lucide-react";
import { useQueryClient } from "@tanstack/react-query";
import { meQueryOptions } from "@/lib/queries/me";
import { useI18n } from "@/lib/i18n";
import { cn } from "@/lib/utils";
import { ThemeSelector, UserMenu } from "@/components/SiteHeader";

const LEFT_COLLAPSED_KEY = "stella-left-collapsed";

export function AppLayout() {
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const queryClient = useQueryClient();
  const me = queryClient.getQueryData(meQueryOptions.queryKey);
  const { t } = useI18n();

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
        <div className="flex min-w-0 items-center gap-3">
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
            <img src="/stella-monogram.svg" alt="" width={24} height={24} className="rounded-sm" />
            <span className="font-serif text-xl italic tracking-tight text-foreground select-none">
              stella
            </span>
          </Link>
          <nav className="ml-2 hidden h-[52px] items-center md:flex">
            {navItems.map((item) => (
              <HeaderNavLink
                key={item.href}
                href={item.href}
                label={item.label}
                pathname={pathname}
              />
            ))}
          </nav>
        </div>

        <div className="flex items-center gap-1.5">
          <ThemeSelector />
          {me && <UserMenu />}
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

function HeaderNavLink({
  href,
  label,
  pathname,
}: {
  href: string;
  label: string;
  pathname: string;
}) {
  const active = pathname === href || pathname.startsWith(href + "/");
  return (
    <Link
      to={href as never}
      className={cn(
        "relative flex h-full items-center px-3 text-sm font-medium transition-colors",
        active ? "text-foreground" : "text-muted-foreground hover:text-foreground",
      )}
    >
      {label}
      {active && (
        <span className="absolute right-3 bottom-0 left-3 h-0.5 rounded-full bg-foreground" />
      )}
    </Link>
  );
}
