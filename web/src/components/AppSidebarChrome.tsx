import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useRouterState } from "@tanstack/react-router";
import {
  Bell,
  ChevronsUpDown,
  ChevronDown,
  FileText,
  LogOut,
  Search,
  Settings,
  UserCog,
} from "lucide-react";
import { logout as logoutRequest } from "@/lib/api-client/sdk.gen";
import { useI18n, SUPPORTED_LOCALES } from "@/lib/i18n";
import { meQueryOptions } from "@/lib/queries/me";
import { inboxQueryOptions } from "@/lib/queries/inbox";
import { ThemeAppearanceControl } from "@/components/ThemeControls";
import { useGlobalSearch } from "@/components/GlobalSearch";
import { Avatar, AvatarFallback } from "@/components/ui/avatar";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
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

const INBOX_KIND_LABELS = {
  blocked: "inbox.kind.blocked",
  review: "inbox.kind.review",
  failed: "inbox.kind.failed",
} as const;

interface AppEntry {
  key: string;
  label: string;
  to: string;
  prefixes: string[];
}

function useApps(): AppEntry[] {
  const { t } = useI18n();
  return [
    { key: "agents", label: t("nav.agents"), to: "/agents", prefixes: ["/agents", "/groups"] },
    { key: "recally", label: t("nav.recally"), to: "/recally", prefixes: ["/recally"] },
  ];
}

function isActive(app: AppEntry, pathname: string): boolean {
  return app.prefixes.some((p) => pathname === p || pathname.startsWith(`${p}/`));
}

/**
 * The sidebar's top row: which app you are in, plus the two global controls that
 * belong to no page (search, inbox). The app switcher is a menu rather than a
 * tab strip because the sidebar column has no room for one, and because the set
 * of apps is small and rarely switched.
 */
export function AppChromeHeader() {
  const { t } = useI18n();
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const apps = useApps();
  const openSearch = useGlobalSearch();
  const activeApp = apps.find((app) => isActive(app, pathname)) ?? apps[0];

  return (
    <div className="flex min-w-0 items-center gap-1">
      <DropdownMenu>
        <DropdownMenuTrigger
          render={<Button variant="ghost" size="sm" className="min-w-0 flex-1 justify-start" />}
        >
          <img src="/stella-monogram.svg" alt="" width={20} height={20} className="rounded-sm" />
          <span className="min-w-0 truncate">{activeApp.label}</span>
          <ChevronDown />
        </DropdownMenuTrigger>
        <DropdownMenuContent align="start" sideOffset={6} className="w-52">
          <DropdownMenuGroup>
            {apps.map((app) => (
              <DropdownMenuItem key={app.key} render={<Link to={app.to as never} />}>
                {app.label}
              </DropdownMenuItem>
            ))}
          </DropdownMenuGroup>
        </DropdownMenuContent>
      </DropdownMenu>

      <div className="flex shrink-0 items-center gap-0.5">
        <Button
          variant="ghost"
          size="icon-sm"
          onClick={openSearch}
          aria-label={t("search.open")}
          title={t("search.open")}
        >
          <Search />
        </Button>
        <InboxBell />
      </div>
    </div>
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
            title={t("inbox.title")}
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
      <PopoverPopup align="start" sideOffset={8} className="w-80 p-2">
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

/**
 * The sidebar's pinned bottom row: who you are signed in as, and everything that
 * hangs off that — personal settings, appearance, sign-out. The appearance
 * switch lives inline in the menu rather than behind its own trigger (nesting a
 * popover inside a menu is a bug) and stays a single row so the menu fits.
 */
export function AppChromeFooter() {
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
      <DropdownMenuTrigger
        render={<Button variant="ghost" size="sm" className="w-full min-w-0 justify-start" />}
      >
        <Avatar className="size-6">
          {/* Not font-mono: a mono capital O is indistinguishable from a zero. */}
          <AvatarFallback className="text-xs font-semibold">{initial}</AvatarFallback>
        </Avatar>
        {/* Usernames can be opaque provider IDs — one line, ellipsis. */}
        <span className="min-w-0 flex-1 truncate text-left">{me.username}</span>
        <ChevronsUpDown />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" side="top" sideOffset={8} className="w-64">
        <DropdownMenuGroup>
          <DropdownMenuLabel>
            <div className="flex min-w-0 flex-col gap-0.5">
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
