import { Link, useNavigate, useRouterState } from "@tanstack/react-router";
import { useI18n } from "@/lib/i18n";
import { cn } from "@/lib/utils";
import { ThemeSelector, UserMenu } from "@/components/SiteHeader";

interface AppSidebarProps {
  children?: React.ReactNode;
  className?: string;
}

export function AppSidebar({ children, className }: AppSidebarProps) {
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const navigate = useNavigate();
  const { t } = useI18n();

  return (
    <aside className={cn("flex h-full w-full flex-col overflow-hidden", className)}>
      <div className="shrink-0 px-3 pt-3">
        <Link to="/agents" className="mb-3 flex items-center gap-2.5 px-2" aria-label="Stella home">
          <img src="/stella-monogram.svg" alt="" width={24} height={24} className="rounded-sm" />
          <span className="font-serif text-xl italic tracking-tight text-foreground select-none">
            stella
          </span>
        </Link>
        <SectionLabel>Apps</SectionLabel>
        <div className="grid gap-0.5">
          <AppNavItem
            active={pathname.startsWith("/agents")}
            icon={<IconAgents />}
            label={t("nav.sessions")}
            onClick={() => void navigate({ to: "/agents" })}
          />
          <AppNavItem
            active={pathname.startsWith("/recally")}
            icon={<IconRecally />}
            label={t("nav.recally")}
            onClick={() => void navigate({ to: "/recally" })}
          />
        </div>
      </div>

      {children}

      <div className="mt-auto shrink-0 border-t border-border/60 px-3 py-2">
        <div className="flex items-center gap-1.5">
          <button
            type="button"
            onClick={() => void navigate({ to: "/settings" })}
            className={cn(
              "flex h-8 min-w-0 flex-1 items-center gap-2 rounded-xl px-2.5 text-left text-sm font-medium tracking-[-0.01em] transition-colors",
              pathname.startsWith("/settings")
                ? "bg-accent text-primary"
                : "text-muted-foreground hover:bg-foreground/[0.045] hover:text-foreground",
            )}
          >
            <span
              className={cn(
                "shrink-0",
                pathname.startsWith("/settings") ? "text-primary" : "text-muted-foreground/70",
              )}
            >
              <IconSettings />
            </span>
            <span className="truncate">{t("nav.settings")}</span>
          </button>
          <ThemeSelector />
          <UserMenu />
        </div>
      </div>
    </aside>
  );
}

export function SectionLabel({ children }: { children: React.ReactNode }) {
  return (
    <div className="px-2 pb-1.5 pt-4 font-mono text-[10px] uppercase tracking-[0.08em] text-muted-foreground/60">
      {children}
    </div>
  );
}

function AppNavItem({
  active,
  icon,
  label,
  onClick,
}: {
  active: boolean;
  icon: React.ReactNode;
  label: string;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        "flex min-h-9 items-center gap-2 rounded-xl px-2.5 py-2 text-left text-sm font-medium tracking-[-0.01em] transition-colors",
        active
          ? "bg-accent text-primary"
          : "text-muted-foreground hover:bg-foreground/[0.045] hover:text-foreground",
      )}
    >
      <span className={cn("shrink-0", active ? "text-primary" : "text-muted-foreground/70")}>
        {icon}
      </span>
      <span className="truncate">{label}</span>
    </button>
  );
}

function IconAgents() {
  return (
    <svg className="size-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8">
      <path d="M16 21v-2a4 4 0 0 0-8 0v2" />
      <circle cx="12" cy="7" r="4" />
      <path d="M22 21v-2a4 4 0 0 0-3-3.87" />
      <path d="M16 3.13a4 4 0 0 1 0 7.75" />
    </svg>
  );
}

function IconRecally() {
  return (
    <svg className="size-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8">
      <path d="M4 5.5A2.5 2.5 0 0 1 6.5 3H20v16H6.5A2.5 2.5 0 0 1 4 16.5v-11Z" />
      <path d="M8 7h8M8 11h6" />
      <path d="M4 16.5A2.5 2.5 0 0 1 6.5 14H20" />
    </svg>
  );
}

function IconSettings() {
  return (
    <svg className="size-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8">
      <circle cx="12" cy="12" r="3" />
      <path d="M19.4 15a1.7 1.7 0 0 0 .34 1.88l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06A1.7 1.7 0 0 0 15 19.4a1.7 1.7 0 0 0-1 .6V20a2 2 0 1 1-4 0v-.08a1.7 1.7 0 0 0-1-.52 1.7 1.7 0 0 0-1.88.34l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06A1.7 1.7 0 0 0 4.6 15a1.7 1.7 0 0 0-.6-1H4a2 2 0 1 1 0-4h.08a1.7 1.7 0 0 0 .52-1 1.7 1.7 0 0 0-.34-1.88l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06A1.7 1.7 0 0 0 9 4.6a1.7 1.7 0 0 0 1-.6V4a2 2 0 1 1 4 0v.08a1.7 1.7 0 0 0 1 .52 1.7 1.7 0 0 0 1.88-.34l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06A1.7 1.7 0 0 0 19.4 9c.23.34.43.67.6 1H20a2 2 0 1 1 0 4h-.08c-.17.33-.37.66-.52 1z" />
    </svg>
  );
}
