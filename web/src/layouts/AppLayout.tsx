import React from "react";
import { Link, Outlet, useNavigate, useRouterState } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { meQueryOptions } from "@/lib/queries/me";
import { queryClient } from "@/lib/queryClient";
import { Separator } from "@/components/ui/separator";

const navItems = [
  { id: "sessions", label: "Sessions", href: "/sessions", adminOnly: false },
  { id: "scheduler", label: "Scheduler", href: "/scheduler", adminOnly: false },
  { id: "settings", label: "Settings", href: "/settings", adminOnly: false },
];

export function AppLayout() {
  const { data: me } = useQuery(meQueryOptions);
  const navigate = useNavigate();
  const pathname = useRouterState({ select: (s) => s.location.pathname });

  const visibleNavItems = navItems.filter((item) => !item.adminOnly || me?.is_admin);

  async function logout() {
    await fetch("/api/auth/logout", { method: "POST", credentials: "same-origin" });
    queryClient.clear();
    navigate({ to: "/login" });
  }

  function handleMobileNav(e: React.ChangeEvent<HTMLSelectElement>) {
    const href = e.target.value;
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    if (href) navigate({ to: href as any });
  }

  return (
    <div className="min-h-screen bg-background text-foreground flex flex-col">
      <header className="border-b border-border">
        <div className="px-6 h-14 flex items-center">
          <div className="flex items-baseline gap-6">
            {/* Logo */}
            <Link
              // eslint-disable-next-line @typescript-eslint/no-explicit-any
              to={"/sessions" as any}
              className="font-serif italic text-primary text-xl tracking-tight select-none"
            >
              anna
            </Link>

            {/* Desktop nav */}
            <nav className="hidden sm:flex items-baseline gap-1 text-sm">
              {visibleNavItems.map((item, i) => (
                <React.Fragment key={item.id}>
                  {i > 0 && <span className="text-muted-foreground mx-2">/</span>}
                  <Link
                    // eslint-disable-next-line @typescript-eslint/no-explicit-any
                    to={item.href as any}
                    className="py-1 font-medium transition-colors"
                    activeProps={{ className: "text-primary py-1 font-medium transition-colors" }}
                    inactiveProps={{
                      className:
                        "text-muted-foreground hover:text-foreground py-1 font-medium transition-colors",
                    }}
                  >
                    {item.label}
                  </Link>
                </React.Fragment>
              ))}
            </nav>

            {/* Mobile nav */}
            <select
              className="sm:hidden text-sm border border-border rounded-md px-2 py-1 bg-background"
              value={pathname}
              onChange={handleMobileNav}
            >
              {visibleNavItems.map((item) => (
                <option key={item.id} value={item.href}>
                  {item.label}
                </option>
              ))}
            </select>
          </div>

          {/* User menu */}
          <div className="ml-auto flex items-center gap-4">
            {me && (
              <details className="relative">
                <summary className="cursor-pointer list-none flex items-center gap-2 px-2 py-1 rounded-lg hover:bg-accent text-sm font-medium">
                  <div className="flex h-7 w-7 items-center justify-center rounded-full bg-muted text-xs font-mono font-semibold">
                    {me.username[0]?.toUpperCase()}
                  </div>
                  <span className="hidden md:inline text-sm text-muted-foreground">
                    {me.username}
                  </span>
                </summary>
                <div className="absolute right-0 top-full mt-2 w-56 rounded-lg border border-border bg-popover p-2 shadow-lg z-50">
                  <div className="px-3 py-2">
                    <p className="text-sm font-medium">{me.username}</p>
                    {me.is_admin && <span className="text-xs text-muted-foreground">admin</span>}
                  </div>
                  <Separator className="my-2" />
                  <Link
                    // eslint-disable-next-line @typescript-eslint/no-explicit-any
                    to={"/settings/account" as any}
                    className="flex w-full items-center rounded-md px-3 py-2 text-sm hover:bg-accent transition-colors"
                  >
                    Account settings
                  </Link>
                  <Separator className="my-2" />
                  <button
                    onClick={logout}
                    className="flex w-full items-center rounded-md px-3 py-2 text-sm text-destructive hover:bg-destructive/10 transition-colors"
                  >
                    Log out
                  </button>
                </div>
              </details>
            )}
          </div>
        </div>
      </header>
      <main className="flex-1 w-full overflow-hidden">
        <Outlet />
      </main>
    </div>
  );
}
