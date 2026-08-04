import { useEffect, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useRouterState } from "@tanstack/react-router";
import {
  Bell,
  ChevronDown,
  FileText,
  LogOut,
  MessagesSquare,
  Search,
  Settings,
  UserCog,
} from "lucide-react";
import { logout as logoutRequest } from "@/lib/api-client/sdk.gen";
import { useI18n, SUPPORTED_LOCALES } from "@/lib/i18n";
import { meQueryOptions } from "@/lib/queries/me";
import { inboxQueryOptions } from "@/lib/queries/inbox";
import { ThemeAppearanceControl } from "@/components/ThemeControls";
import { GlobalSearchDialog } from "@/components/GlobalSearchDialog";
import { Avatar, AvatarFallback } from "@/components/ui/avatar";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Kbd } from "@/components/ui/kbd";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/menu";
import { Popover, PopoverPopup, PopoverTrigger } from "@/components/ui/popover";
import { Separator } from "@/components/ui/separator";
import { AppHeaderSlotTarget } from "@/layouts/AppHeaderSlot";

const INBOX_KIND_LABELS = {
  blocked: "inbox.kind.blocked",
  review: "inbox.kind.review",
  failed: "inbox.kind.failed",
} as const;

interface AppTab {
  key: string;
  label: string;
  to: string;
  prefixes: string[];
}

function useAppTabs(): AppTab[] {
  const { t } = useI18n();
  return [
    { key: "agents", label: t("nav.agents"), to: "/agents", prefixes: ["/agents", "/groups"] },
    { key: "recally", label: t("nav.recally"), to: "/recally", prefixes: ["/recally"] },
  ];
}

function isActive(tab: AppTab, pathname: string): boolean {
  return tab.prefixes.some((p) => pathname === p || pathname.startsWith(`${p}/`));
}

/**
 * The app's only bar. Left: which app you are in (logo + tabs). Middle: whatever
 * the mounted shell puts there (sidebar trigger, breadcrumb, page actions) via
 * the header slot. Right: what needs you (bell), who you are (avatar menu).
 */
export function GlobalTopBar() {
  const { t } = useI18n();
  const { data: me } = useQuery(meQueryOptions);
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const tabs = useAppTabs();
  const [searchOpen, setSearchOpen] = useState(false);

  useEffect(() => {
    function onKeyDown(event: KeyboardEvent) {
      if (event.key.toLowerCase() === "k" && (event.metaKey || event.ctrlKey)) {
        event.preventDefault();
        setSearchOpen((open) => !open);
      }
    }
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, []);

  if (!me) return null;

  const activeTab = tabs.find((tab) => isActive(tab, pathname));

  return (
    <header className="relative z-30 flex h-14 shrink-0 items-center gap-2 border-b border-border bg-background px-4">
      <Link to="/agents" className="flex shrink-0 items-center gap-2">
        <img src="/stella-monogram.svg" alt="" width={24} height={24} className="rounded-sm" />
        <span className="select-none font-serif text-xl italic tracking-tight max-lg:hidden">
          stella
        </span>
      </Link>

      {/* Desktop: app tabs inline. Mobile: the same set behind one menu. */}
      {/* `secondary` is the active affordance: it reads as a filled tab in both
          light and dark, where a ghost hover tint does not. */}
      <nav className="ml-2 hidden items-center gap-1 sm:flex">
        {tabs.map((tab) => (
          <Button
            key={tab.key}
            variant={isActive(tab, pathname) ? "secondary" : "ghost"}
            size="sm"
            render={<Link to={tab.to as never} />}
          >
            {tab.label}
          </Button>
        ))}
      </nav>
      <DropdownMenu>
        <DropdownMenuTrigger
          render={<Button variant="ghost" size="sm" className="ml-1 sm:hidden" />}
        >
          {activeTab?.label ?? t("nav.agents")}
          <ChevronDown />
        </DropdownMenuTrigger>
        <DropdownMenuContent align="start" sideOffset={6}>
          <DropdownMenuGroup>
            {tabs.map((tab) => (
              <DropdownMenuItem key={tab.key} render={<Link to={tab.to as never} />}>
                <MessagesSquare className="size-4" />
                {tab.label}
              </DropdownMenuItem>
            ))}
          </DropdownMenuGroup>
        </DropdownMenuContent>
      </DropdownMenu>

      {/* Contextual region: the mounted shell portals its sidebar trigger,
          breadcrumb, and page actions in here. Empty on shell-less routes. */}
      <AppHeaderSlotTarget className="flex min-w-0 flex-1 items-center gap-2 pl-2" />

      <div className="flex shrink-0 items-center gap-1">
        <Button
          variant="ghost"
          size="sm"
          onClick={() => setSearchOpen(true)}
          aria-label={t("search.open")}
        >
          <Search />
          {/* The middle region now competes for the same row: below xl the
              search trigger drops to its icon so the breadcrumb keeps its width. */}
          <span className="max-xl:hidden">{t("search.open")}</span>
          <Kbd className="max-xl:hidden">⌘K</Kbd>
        </Button>
        <InboxBell />
        <AppUserMenu />
      </div>

      <GlobalSearchDialog open={searchOpen} onOpenChange={setSearchOpen} />
    </header>
  );
}

// The old sidebar's "Needs you" section, relocated: one bell, one count, one
// popover that deep-links straight at the blocked goal or failed run.
function InboxBell() {
  const { t } = useI18n();
  const { data: inbox } = useQuery(inboxQueryOptions(undefined, 20));
  const items = inbox?.items ?? [];

  return (
    <Popover>
      <PopoverTrigger
        render={
          <Button
            variant="ghost"
            size="icon-sm"
            className="relative"
            aria-label={t("inbox.title")}
          />
        }
      >
        <Bell />
        {items.length > 0 && (
          <Badge size="sm" className="absolute -end-1 -top-1">
            {items.length}
          </Badge>
        )}
      </PopoverTrigger>
      <PopoverPopup align="end" sideOffset={8} className="w-80 p-2">
        <div className="flex items-center justify-between px-2 py-1">
          <span className="text-xs text-muted-foreground">{t("inbox.title")}</span>
          <Button variant="ghost" size="xs" render={<Link to="/inbox" />}>
            {t("inbox.viewAll")}
          </Button>
        </div>
        <Separator />
        <div className="mt-1 flex max-h-80 flex-col gap-0.5 overflow-y-auto">
          {items.length === 0 ? (
            <p className="px-2 py-6 text-center text-sm text-muted-foreground">
              {t("inbox.empty")}
            </p>
          ) : (
            items.map((item) => (
              <Button
                key={item.id}
                variant="ghost"
                size="sm"
                className="h-auto w-full justify-start py-2"
                render={<Link to={item.target_path} />}
              >
                <span className="flex min-w-0 flex-1 flex-col gap-0.5 text-left">
                  <span className="truncate text-sm">{item.title}</span>
                  <span className="truncate text-xs text-muted-foreground">
                    {item.detail || t(INBOX_KIND_LABELS[item.kind])}
                  </span>
                </span>
              </Button>
            ))
          )}
        </div>
      </PopoverPopup>
    </Popover>
  );
}

// Identity, personal settings, appearance, and sign-out. The appearance switch
// lives inline here rather than behind its own trigger — nesting a popover
// inside a menu is a bug — and stays a single row so the menu fits the viewport.
function AppUserMenu() {
  const { data: me } = useQuery(meQueryOptions);
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const { t, locale, setLocale } = useI18n();

  if (!me) return null;

  async function logout() {
    await logoutRequest({ throwOnError: true });
    queryClient.clear();
    void navigate({ to: "/login" });
  }

  const nextLocaleLabel = locale === "en" ? t("locale.zh") : t("locale.en");
  const initial = me.username.trim()[0]?.toUpperCase() ?? "?";

  return (
    <DropdownMenu>
      <DropdownMenuTrigger className="ml-1 flex cursor-pointer items-center rounded-lg p-1 outline-none transition-colors hover:bg-accent">
        <Avatar className="size-7">
          {/* Not font-mono: a mono capital O is indistinguishable from a zero. */}
          <AvatarFallback className="text-xs font-semibold">{initial}</AvatarFallback>
        </Avatar>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" sideOffset={8} className="w-64">
        <DropdownMenuGroup>
          <DropdownMenuLabel>
            <div className="flex min-w-0 flex-col gap-0.5">
              {/* Usernames can be opaque provider IDs — one line, ellipsis, so the
                  menu never grows a horizontal scrollbar. */}
              <span className="truncate text-sm font-medium text-foreground" title={me.username}>
                {me.username}
              </span>
              {me.is_admin && <span className="text-xs text-muted-foreground">admin</span>}
            </div>
          </DropdownMenuLabel>
        </DropdownMenuGroup>
        <DropdownMenuSeparator />
        <DropdownMenuGroup>
          <DropdownMenuItem render={<Link to="/settings/account" />}>
            <UserCog className="size-4" />
            {t("settings.nav.account")}
          </DropdownMenuItem>
          <DropdownMenuItem render={<Link to="/settings" />}>
            <Settings className="size-4" />
            {t("nav.settings")}
          </DropdownMenuItem>
          <DropdownMenuItem render={<Link to={"/docs" as never} />}>
            <FileText className="size-4" />
            {t("nav.docs")}
          </DropdownMenuItem>
          <DropdownMenuItem
            onClick={() => void setLocale(SUPPORTED_LOCALES.find((l) => l !== locale) ?? locale)}
          >
            <span className="flex size-4 items-center justify-center text-xs font-medium">文</span>
            {nextLocaleLabel}
          </DropdownMenuItem>
        </DropdownMenuGroup>
        <DropdownMenuSeparator />
        {/* Only the light/dark/system switch lives here — it is a one-tap toggle.
            Accent color is a rarer, wider control and lives on the account page. */}
        <div className="p-2">
          <ThemeAppearanceControl layout="inline" />
        </div>
        <DropdownMenuSeparator />
        <DropdownMenuGroup>
          <DropdownMenuItem className="text-destructive focus:text-destructive" onClick={logout}>
            <LogOut className="size-4" />
            {t("header.logout")}
          </DropdownMenuItem>
        </DropdownMenuGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
