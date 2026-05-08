import React from "react";
import { Link, useNavigate, useRouterState } from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { meQueryOptions } from "@/lib/queries/me";
import { queryClient } from "@/lib/queryClient";
import { Separator } from "@/components/ui/separator";
import type { ReactNode } from "react";

const appNavItems = [
  { label: "Sessions", href: "/sessions" },
  { label: "Scheduler", href: "/scheduler" },
  { label: "Settings", href: "/settings" },
];

interface SiteHeaderProps {
  variant: "landing" | "docs" | "dashboard";
  trailing?: ReactNode;
}

export function SiteHeader({ variant, trailing }: SiteHeaderProps) {
  const pathname = useRouterState({ select: (s) => s.location.pathname });

  return (
    <header className="border-b border-border h-14 flex items-center px-6 bg-background shrink-0">
      <Link to="/" className="flex items-center gap-2 shrink-0">
        <img src="/anna-monogram.svg" alt="" width={24} height={24} className="rounded-sm" />
        <span className="font-serif italic text-xl tracking-tight select-none">anna</span>
      </Link>

      {variant === "dashboard" ? (
        <DashboardNav pathname={pathname} />
      ) : (
        <PublicNav pathname={pathname} />
      )}

      <div className="flex-1" />

      <div className="flex items-center gap-3">
        {variant === "dashboard" ? <DashboardActions /> : <PublicActions />}
        <GithubLink />
        {variant === "dashboard" && <UserMenu />}
        {trailing}
      </div>
    </header>
  );
}

function PublicNav({ pathname }: { pathname: string }) {
  return (
    <nav className="flex items-baseline gap-1 text-sm ml-6">
      <NavLink href="/docs" label="Docs" pathname={pathname} />
    </nav>
  );
}

function DashboardNav({ pathname }: { pathname: string }) {
  return (
    <>
      <nav className="hidden sm:flex items-baseline gap-1 text-sm ml-6">
        {appNavItems.map((item, i) => (
          <React.Fragment key={item.href}>
            {i > 0 && <span className="text-muted-foreground mx-2">/</span>}
            <NavLink href={item.href} label={item.label} pathname={pathname} />
          </React.Fragment>
        ))}
      </nav>
      <MobileSelect items={appNavItems} pathname={pathname} />
    </>
  );
}

function NavLink({ href, label, pathname }: { href: string; label: string; pathname: string }) {
  const active = pathname === href || pathname.startsWith(href + "/");
  return (
    <Link
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      to={href as any}
      className={`py-1 font-medium transition-colors ${
        active ? "text-foreground" : "text-muted-foreground hover:text-foreground"
      }`}
    >
      {label}
    </Link>
  );
}

function MobileSelect({
  items,
  pathname,
}: {
  items: { label: string; href: string }[];
  pathname: string;
}) {
  const navigate = useNavigate();
  return (
    <select
      className="sm:hidden ml-4 text-sm border border-border rounded-md px-2 py-1 bg-background"
      value={items.find((i) => pathname.startsWith(i.href))?.href ?? items[0]?.href}
      onChange={(e) => {
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        if (e.target.value) navigate({ to: e.target.value as any });
      }}
    >
      {items.map((item) => (
        <option key={item.href} value={item.href}>
          {item.label}
        </option>
      ))}
    </select>
  );
}

function PublicActions() {
  const qc = useQueryClient();
  const me = qc.getQueryData(meQueryOptions.queryKey);
  return (
    <>
      <NavLink
        href="/about"
        label="About"
        pathname={useRouterState({ select: (s) => s.location.pathname })}
      />
      {me ? (
        <Link
          // eslint-disable-next-line @typescript-eslint/no-explicit-any
          to={"/sessions" as any}
          className="text-sm text-muted-foreground hover:text-foreground font-medium transition-colors"
        >
          Dashboard
        </Link>
      ) : (
        <Link
          to="/login"
          className="text-sm text-muted-foreground hover:text-foreground font-medium transition-colors"
        >
          Login
        </Link>
      )}
    </>
  );
}

function DashboardActions() {
  return (
    <Link
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      to={"/docs/$" as any}
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      params={{ _splat: "" } as any}
      className="text-sm text-muted-foreground hover:text-foreground font-medium transition-colors"
    >
      Docs
    </Link>
  );
}

function GithubLink() {
  return (
    <a
      href="https://github.com/vaayne/anna"
      target="_blank"
      rel="noopener noreferrer"
      className="text-muted-foreground hover:text-foreground transition-colors"
    >
      <svg viewBox="0 0 24 24" width="20" height="20" fill="currentColor">
        <path d="M12 0C5.37 0 0 5.37 0 12c0 5.31 3.435 9.795 8.205 11.385.6.105.825-.255.825-.57 0-.285-.015-1.23-.015-2.235-3.015.555-3.795-.735-4.035-1.41-.135-.345-.72-1.41-1.23-1.695-.42-.225-1.02-.78-.015-.795.945-.015 1.62.87 1.845 1.23 1.08 1.815 2.805 1.305 3.495.99.105-.78.42-1.305.765-1.605-2.67-.3-5.46-1.335-5.46-5.925 0-1.305.465-2.385 1.23-3.225-.12-.3-.54-1.53.12-3.18 0 0 1.005-.315 3.3 1.23.96-.27 1.98-.405 3-.405s2.04.135 3 .405c2.295-1.56 3.3-1.23 3.3-1.23.66 1.65.24 2.88.12 3.18.765.84 1.23 1.905 1.23 3.225 0 4.605-2.805 5.625-5.475 5.925.435.375.81 1.095.81 2.22 0 1.605-.015 2.895-.015 3.3 0 .315.225.69.825.57A12.02 12.02 0 0024 12c0-6.63-5.37-12-12-12z" />
      </svg>
    </a>
  );
}

function UserMenu() {
  const { data: me } = useQuery(meQueryOptions);
  const navigate = useNavigate();

  if (!me) return null;

  async function logout() {
    await fetch("/api/auth/logout", { method: "POST", credentials: "same-origin" });
    queryClient.clear();
    navigate({ to: "/login" });
  }

  return (
    <details className="relative">
      <summary className="cursor-pointer list-none flex items-center gap-2 px-2 py-1 rounded-lg hover:bg-accent text-sm font-medium">
        <div className="flex h-7 w-7 items-center justify-center rounded-full bg-muted text-xs font-mono font-semibold">
          {me.username[0]?.toUpperCase()}
        </div>
        <span className="hidden md:inline text-sm text-muted-foreground">{me.username}</span>
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
  );
}
