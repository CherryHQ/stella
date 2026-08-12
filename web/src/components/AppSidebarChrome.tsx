import { useRef } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useRouterState } from "@tanstack/react-router";
import {
  Bell,
  ChevronsUpDown,
  FileText,
  LogOut,
  Search,
  Settings,
  ShieldCheck,
} from "lucide-react";
import { cn } from "@/lib/utils";
import { logout as logoutRequest } from "@/lib/api-client/sdk.gen";
import { useI18n, SUPPORTED_LOCALES } from "@/lib/i18n";
import { meQueryOptions } from "@/lib/queries/me";
import { INBOX_SOURCE_LABELS, inboxQueryOptions, useInboxAgentName } from "@/lib/queries/inbox";
import { ThemeAppearanceControl } from "@/components/ThemeControls";
import { SegmentedField } from "@/components/SegmentedField";
import { useGlobalSearch } from "@/components/GlobalSearch";
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar";
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
 * The sidebar's top row: where you can work, plus the two global controls that
 * belong to no page (search, inbox). With only two workspaces, both stay
 * visible as a segmented switch — a menu hides the other destination behind an
 * extra click for no gain. If the set outgrows this row, the upgrade is an
 * icon rail, not a longer strip.
 */
export function AppChromeHeader() {
  const { t } = useI18n();
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const apps = useApps();
  const openSearch = useGlobalSearch();
  const activeApp = apps.find((app) => isActive(app, pathname)) ?? apps[0];

  return (
    <div className="flex min-w-0 items-center gap-2">
      <img
        src="/stella-monogram.svg"
        alt="Stella"
        width={20}
        height={20}
        className="shrink-0 rounded-sm"
      />
      <nav className="flex min-w-0 items-center gap-0.5 rounded-lg bg-muted p-0.5">
        {apps.map((app) => {
          const active = app.key === activeApp.key;
          return (
            <Link
              key={app.key}
              to={app.to as never}
              aria-current={active ? "page" : undefined}
              className={cn(
                "min-w-0 truncate whitespace-nowrap rounded-md px-2.5 py-1 text-xs font-medium transition-colors",
                active
                  ? "bg-card text-foreground shadow-sm"
                  : "text-muted-foreground hover:text-foreground",
              )}
            >
              {app.label}
            </Link>
          );
        })}
      </nav>

      <div className="ms-auto flex shrink-0 items-center gap-0.5">
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
  const agentName = useInboxAgentName();
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
            items.map((item) => {
              const agent = agentName(item.agent_id);
              return (
                <Button
                  key={item.id}
                  variant="ghost"
                  size="sm"
                  // A two-line row has to outgrow the size variant's fixed
                  // height, and the variant pins it twice — `h-8 sm:h-7`. Only
                  // naming both frees it; `h-auto` alone leaves `sm:h-7`
                  // standing, which clipped the title and put a scrollbar on a
                  // single-item list.
                  className="h-auto w-full justify-start py-2 sm:h-auto"
                  render={<Link to={item.target_path} />}
                >
                  <span className="flex min-w-0 flex-1 flex-col gap-0.5 text-left">
                    <span className="truncate text-sm">{item.title}</span>
                    <span className="truncate text-xs text-muted-foreground">
                      {agent ? `${agent} · ` : ""}
                      {item.detail || t(INBOX_SOURCE_LABELS[item.source_type])}
                    </span>
                  </span>
                </Button>
              );
            })
          )}
        </div>
      </PopoverPopup>
    </Popover>
  );
}

// Locale is a preference, not a destination, so it gets the same row shape as
// appearance instead of a menu item. As an item it had to be worded as the
// language you would switch *to*, which is indistinguishable from the language
// you are already in — the segments show both and mark the live one.
function LocaleField() {
  const { t, locale, setLocale } = useI18n();

  return (
    <SegmentedField
      label={t("header.language")}
      value={locale}
      onChange={(next) => void setLocale(next)}
      // Each locale names itself: 中文 is legible to someone who cannot read the
      // English around it, which is exactly who needs this row.
      options={SUPPORTED_LOCALES.map((l) => ({ value: l, label: t(`locale.${l}`) }))}
    />
  );
}

/**
 * The sidebar's pinned bottom row: who you are signed in as, and everything that
 * hangs off that — where to go (settings, account, docs), how it should look and
 * read (appearance, locale), and the way out.
 *
 * One row, not two. Settings used to sit outside the menu as its own row, which
 * put the broadest destination in the app beside the narrowest one and made the
 * footer two competing anchors. Inside, it leads the destinations and the
 * identity pill is the sidebar's single bottom edge.
 *
 * The menu is a decision surface: everything in it is one click, visible, and
 * reversible. That is why the accent picker is not here — a 360° hue slider is
 * something you explore, and it already has room on the account page.
 */
export function AppChromeFooter() {
  const { data: me } = useQuery(meQueryOptions);
  // The menu anchors to the whole footer row, not the trigger button, so its
  // width tracks the sidebar column instead of drifting past its edge.
  const anchorRef = useRef<HTMLDivElement>(null);
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const { t } = useI18n();

  if (!me) return null;

  async function logout() {
    await logoutRequest({ throwOnError: true });
    queryClient.clear();
    void navigate({ to: "/login" });
  }

  // The display name is what a human recognises; the username stays reachable in
  // the menu header because it is the identity you actually sign in with.
  const name = me.name?.trim();
  const displayName = name || me.username;
  const initial = displayName.trim()[0]?.toUpperCase() ?? "?";

  return (
    <div ref={anchorRef} className="flex min-w-0 flex-col">
      <DropdownMenu>
        <DropdownMenuTrigger
          render={<Button variant="ghost" size="sm" className="w-full min-w-0 justify-start" />}
        >
          <Avatar className="size-6">
            {me.avatar_url && <AvatarImage src={me.avatar_url} alt="" />}
            {/* Not font-mono: a mono capital O is indistinguishable from a zero. */}
            <AvatarFallback className="text-xs font-semibold">{initial}</AvatarFallback>
          </Avatar>
          {/* Usernames can be opaque provider IDs — one line, ellipsis. */}
          <span className="min-w-0 flex-1 truncate text-left" title={displayName}>
            {displayName}
          </span>
          <ChevronsUpDown />
        </DropdownMenuTrigger>
        <DropdownMenuContent
          anchor={anchorRef}
          align="start"
          side="top"
          sideOffset={8}
          className="w-(--anchor-width)"
        >
          <DropdownMenuGroup>
            <DropdownMenuLabel>
              {/* The identity card mirrors the trigger: avatar, human name, an
                  admin badge — and the sign-in username demoted to fine print. */}
              <div className="flex min-w-0 items-center gap-2.5">
                <Avatar className="size-8 shrink-0">
                  {me.avatar_url && <AvatarImage src={me.avatar_url} alt="" />}
                  <AvatarFallback className="text-xs font-semibold">{initial}</AvatarFallback>
                </Avatar>
                <div className="flex min-w-0 flex-1 flex-col">
                  <div className="flex min-w-0 items-center gap-1.5">
                    <span
                      className="truncate text-sm font-medium text-foreground"
                      title={displayName}
                    >
                      {displayName}
                    </span>
                    {me.is_admin && (
                      <Badge variant="secondary" size="sm">
                        admin
                      </Badge>
                    )}
                  </div>
                  {name && (
                    <span className="truncate text-xs text-muted-foreground" title={me.username}>
                      {me.username}
                    </span>
                  )}
                </div>
              </div>
            </DropdownMenuLabel>
          </DropdownMenuGroup>
          <DropdownMenuSeparator />
          {/* Personal and deployment controls are separate destinations. The
              server remains the security boundary; this role check only keeps
              an irrelevant destination out of the menu. */}
          <DropdownMenuGroup>
            <DropdownMenuItem render={<Link to="/settings" />}>
              <Settings className="size-4" />
              {t("nav.personalSettings")}
            </DropdownMenuItem>
            {me.is_admin && (
              <DropdownMenuItem render={<Link to="/admin" />}>
                <ShieldCheck className="size-4" />
                {t("nav.adminConsole")}
              </DropdownMenuItem>
            )}
            <DropdownMenuItem render={<Link to={"/docs" as never} />}>
              <FileText className="size-4" />
              {t("nav.docs")}
            </DropdownMenuItem>
          </DropdownMenuGroup>
          <DropdownMenuSeparator />
          {/* Preferences, not destinations: two rows of the same shape, so the
              block reads as one idea and the controls share a right edge. */}
          <div className="flex flex-col gap-2 p-2">
            <ThemeAppearanceControl layout="inline" />
            <LocaleField />
          </div>
          <DropdownMenuSeparator />
          <DropdownMenuGroup>
            <DropdownMenuItem
              className="text-destructive-foreground focus:text-destructive-foreground"
              onClick={logout}
            >
              <LogOut className="size-4" />
              {t("header.logout")}
            </DropdownMenuItem>
          </DropdownMenuGroup>
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  );
}
