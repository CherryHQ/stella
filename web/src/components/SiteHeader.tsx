import { Link, useNavigate, useRouterState } from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { FileText, LogOut, Menu as MenuIcon } from "lucide-react";
import { siGithub } from "simple-icons";
import { useState } from "react";
import { logout as logoutRequest } from "@/lib/api-client/sdk.gen";
import { meQueryOptions } from "@/lib/queries/me";
import { Separator } from "@/components/ui/separator";
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuLabel,
  DropdownMenuGroup,
} from "@/components/ui/menu";
import { Sheet, SheetTrigger, SheetPopup, SheetHeader } from "@/components/ui/sheet";
import { Popover, PopoverTrigger, PopoverPopup } from "@/components/ui/popover";
import { useI18n, SUPPORTED_LOCALES } from "@/lib/i18n";
import { APPEARANCE_ICONS, ThemeControls } from "@/components/ThemeControls";
import { getStoredTheme, type ThemeAppearance } from "@/lib/theme";

export function SiteHeader() {
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const { data: me } = useQuery(meQueryOptions);
  const [sheetOpen, setSheetOpen] = useState(false);
  const { t } = useI18n();

  const appNavItems = [
    { label: t("nav.sessions"), href: "/agents" },
    { label: t("nav.recally"), href: "/recally" },
    { label: t("nav.personalSettings"), href: "/settings" },
  ];

  const utilNavItems = [
    { label: t("nav.docs"), href: "/docs" },
    { label: t("nav.apiReferences"), href: "/api-references", external: true },
  ];

  return (
    <header className="border-b border-border h-14 flex items-center px-6 bg-background shrink-0 relative z-30">
      <Link to="/" className="flex items-center gap-2 shrink-0">
        <img src="/stella-monogram.svg" alt="" width={24} height={24} className="rounded-sm" />
        <span className="font-semibold text-xl tracking-tight select-none">stella</span>
      </Link>

      {/* Desktop nav — app items first, then utility */}
      <nav className="hidden sm:flex items-center h-full ml-6">
        {me &&
          appNavItems.map((item) => (
            <HeaderNavLink
              key={item.href}
              href={item.href}
              label={item.label}
              pathname={pathname}
            />
          ))}
      </nav>

      <div className="flex-1" />

      {/* Right side */}
      <div className="flex items-center gap-1">
        {!me && (
          <>
            <nav className="hidden sm:flex items-center h-full">
              {utilNavItems.map((item) =>
                item.external ? (
                  <a
                    key={item.href}
                    href={item.href}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="relative px-3 h-full flex items-center text-sm font-medium text-muted-foreground hover:text-foreground transition-colors"
                  >
                    {item.label}
                  </a>
                ) : (
                  <HeaderNavLink
                    key={item.href}
                    href={item.href}
                    label={item.label}
                    pathname={pathname}
                  />
                ),
              )}
            </nav>
            <GithubLink />
            <LocaleSelector />
          </>
        )}
        <ThemeMenu />
        {me ? (
          <UserMenu />
        ) : (
          <Button render={<Link to="/login" />} size="sm" className="ml-1">
            {t("header.login")}
          </Button>
        )}

        {/* Mobile hamburger → Sheet */}
        <Sheet open={sheetOpen} onOpenChange={setSheetOpen}>
          <SheetTrigger
            className="sm:hidden p-1.5 rounded-md text-muted-foreground hover:text-foreground hover:bg-accent transition-colors ml-1"
            aria-label={t("header.openNavigation")}
          >
            <MenuIcon className="size-5" />
          </SheetTrigger>
          <SheetPopup side="left" showCloseButton>
            <SheetHeader>
              <Link to="/" className="flex items-center gap-2" onClick={() => setSheetOpen(false)}>
                <img
                  src="/stella-monogram.svg"
                  alt=""
                  width={24}
                  height={24}
                  className="rounded-sm"
                />
                <span className="font-semibold text-xl tracking-tight select-none">stella</span>
              </Link>
            </SheetHeader>
            <nav className="flex flex-col px-4 pb-4">
              {me &&
                appNavItems.map((item) => (
                  <MobileNavLink
                    key={item.href}
                    href={item.href}
                    label={item.label}
                    pathname={pathname}
                    onNavigate={() => setSheetOpen(false)}
                  />
                ))}
              {me && <Separator className="my-2" />}
              {utilNavItems.map((item) =>
                item.external ? (
                  <a
                    key={item.href}
                    href={item.href}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="px-3 py-2.5 rounded-md text-sm font-medium text-muted-foreground hover:text-foreground hover:bg-accent/50 transition-colors"
                    onClick={() => setSheetOpen(false)}
                  >
                    {item.label}
                  </a>
                ) : (
                  <MobileNavLink
                    key={item.href}
                    href={item.href}
                    label={item.label}
                    pathname={pathname}
                    onNavigate={() => setSheetOpen(false)}
                  />
                ),
              )}
            </nav>
          </SheetPopup>
        </Sheet>
      </div>
    </header>
  );
}

// Appearance (system / light / dark) and accent color in one popover — they're
// both "how the app looks", so a single control beats two header buttons.
export function ThemeMenu() {
  const { t } = useI18n();
  const [appearance] = useState<ThemeAppearance>(() => getStoredTheme().appearance);
  const TriggerIcon = APPEARANCE_ICONS[appearance];

  return (
    <Popover>
      <PopoverTrigger
        className="inline-flex items-center rounded-md p-1.5 text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
        aria-label={t("header.appearance")}
        title={t("header.appearance")}
      >
        <TriggerIcon className="size-4" />
      </PopoverTrigger>
      <PopoverPopup align="end" sideOffset={8} className="w-68 space-y-4 p-4">
        <ThemeControls />
      </PopoverPopup>
    </Popover>
  );
}

function LocaleSelector() {
  const { locale, setLocale, t } = useI18n();

  function toggle() {
    void setLocale(SUPPORTED_LOCALES.find((l) => l !== locale) ?? locale);
  }

  const label = locale === "en" ? t("locale.en") : t("locale.zh");

  return (
    <button
      type="button"
      onClick={toggle}
      className="px-2 py-1 rounded-md text-xs font-medium text-muted-foreground hover:text-foreground hover:bg-accent transition-colors"
      aria-label={`Switch language — current: ${label}`}
    >
      {label}
    </button>
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
  // SAFETY: href is the caller's validated app route; coerced to Link's route-union type.
  const to = href as never;
  return (
    <Link
      to={to}
      className={`relative px-3 h-full flex items-center text-sm font-medium transition-colors ${
        active ? "text-foreground" : "text-muted-foreground hover:text-foreground"
      }`}
    >
      {label}
      {active && (
        <span className="absolute bottom-0 left-3 right-3 h-0.5 bg-foreground rounded-full" />
      )}
    </Link>
  );
}

function MobileNavLink({
  href,
  label,
  pathname,
  onNavigate,
}: {
  href: string;
  label: string;
  pathname: string;
  onNavigate: () => void;
}) {
  const active = pathname === href || pathname.startsWith(href + "/");
  // SAFETY: href is the caller's internal app route; coerced to Link's route-union type.
  const to = href as never;
  return (
    <Link
      to={to}
      className={`px-3 py-2.5 rounded-md text-sm font-medium transition-colors ${
        active
          ? "text-foreground bg-accent"
          : "text-muted-foreground hover:text-foreground hover:bg-accent/50"
      }`}
      onClick={onNavigate}
    >
      {label}
    </Link>
  );
}

function GithubLink() {
  const { t } = useI18n();
  return (
    <a
      href="https://github.com/CherryHQ/stella"
      target="_blank"
      rel="noopener noreferrer"
      className="p-1.5 rounded-md text-muted-foreground hover:text-foreground hover:bg-accent transition-colors"
      aria-label={t("header.github")}
    >
      <svg viewBox="0 0 24 24" className="size-4 shrink-0" fill="currentColor" aria-hidden="true">
        <path d={siGithub.path} />
      </svg>
    </a>
  );
}

export function UserMenu() {
  const { data: me } = useQuery(meQueryOptions);
  const qc = useQueryClient();
  const navigate = useNavigate();
  const { t, locale, setLocale } = useI18n();
  // SAFETY: "/docs" is a static app route; coerced to Link's route-union type.
  const docRoute = "/docs" as never;

  if (!me) return null;

  async function logout() {
    await logoutRequest({ throwOnError: true });
    qc.clear();
    void navigate({ to: "/login" });
  }

  function toggleLocale() {
    void setLocale(SUPPORTED_LOCALES.find((l) => l !== locale) ?? locale);
  }

  const nextLocaleLabel = locale === "en" ? t("locale.zh") : t("locale.en");
  // Same identity treatment as the sidebar footer: show the human name, keep the
  // username as the secondary line so the login identity stays discoverable.
  const name = me.name?.trim();
  const displayName = name || me.username;

  return (
    <DropdownMenu>
      <DropdownMenuTrigger className="cursor-pointer flex items-center p-1 rounded-lg hover:bg-accent transition-colors outline-none ml-1">
        <Avatar className="size-7">
          {me.avatar_url && <AvatarImage src={me.avatar_url} alt="" />}
          <AvatarFallback className="text-xs font-mono font-semibold">
            {displayName[0]?.toUpperCase()}
          </AvatarFallback>
        </Avatar>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" sideOffset={8} className="w-52">
        <DropdownMenuGroup>
          <DropdownMenuLabel>
            <div className="flex min-w-0 flex-col gap-0.5">
              <span className="truncate text-sm font-medium text-foreground" title={displayName}>
                {displayName}
              </span>
              {name && (
                <span className="truncate text-xs text-muted-foreground" title={me.username}>
                  {me.username}
                </span>
              )}
              {me.is_admin && <span className="text-xs text-muted-foreground">admin</span>}
            </div>
          </DropdownMenuLabel>
        </DropdownMenuGroup>
        <DropdownMenuSeparator />
        <DropdownMenuGroup>
          <DropdownMenuItem render={<Link to={docRoute} />}>
            <FileText className="size-4" />
            {t("nav.docs")}
          </DropdownMenuItem>
          <DropdownMenuItem
            render={<a href="/api-references" target="_blank" rel="noopener noreferrer" />}
          >
            <FileText className="size-4" />
            {t("nav.apiReferences")}
          </DropdownMenuItem>
          <DropdownMenuItem
            render={
              <a
                href="https://github.com/CherryHQ/stella"
                target="_blank"
                rel="noopener noreferrer"
              />
            }
          >
            <svg
              viewBox="0 0 24 24"
              className="size-4 shrink-0"
              fill="currentColor"
              aria-hidden="true"
            >
              <path d={siGithub.path} />
            </svg>
            GitHub
          </DropdownMenuItem>
          <DropdownMenuItem onClick={toggleLocale}>
            <span className="size-4 flex items-center justify-center text-xs font-medium">文</span>
            {nextLocaleLabel}
          </DropdownMenuItem>
        </DropdownMenuGroup>
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
  );
}
