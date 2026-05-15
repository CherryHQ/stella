import { Link, useNavigate, useRouterState } from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, Menu as MenuIcon, Monitor, Moon, Sun } from "lucide-react";
import { useEffect, useState } from "react";
import { meQueryOptions } from "@/lib/queries/me";
import { Separator } from "@/components/ui/separator";
import { Avatar, AvatarFallback } from "@/components/ui/avatar";
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
import { useI18n, SUPPORTED_LOCALES } from "@/lib/i18n";
import {
  applyTheme,
  getStoredTheme,
  setStoredTheme,
  type ThemeAppearance,
  type ThemeSettings,
} from "@/lib/theme";

export function SiteHeader() {
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const qc = useQueryClient();
  const me = qc.getQueryData(meQueryOptions.queryKey);
  const [sheetOpen, setSheetOpen] = useState(false);
  const { t } = useI18n();

  const appNavItems = [
    { label: t("nav.sessions"), href: "/sessions" },
    { label: t("nav.automations"), href: "/automations" },
    { label: t("nav.recally"), href: "/recally" },
    { label: t("nav.settings"), href: "/settings" },
  ];

  const utilNavItems = [{ label: t("nav.docs"), href: "/docs" }];

  return (
    <header className="border-b border-border h-14 flex items-center px-6 bg-background shrink-0 relative z-30">
      <Link to="/" className="flex items-center gap-2 shrink-0">
        <img src="/stella-monogram.svg" alt="" width={24} height={24} className="rounded-sm" />
        <span className="font-serif italic text-xl tracking-tight select-none">stella</span>
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

      {/* Right side — utility nav, locale selector, GitHub, auth */}
      <div className="flex items-center gap-1">
        <nav className="hidden sm:flex items-center h-full">
          {utilNavItems.map((item) => (
            <HeaderNavLink
              key={item.href}
              href={item.href}
              label={item.label}
              pathname={pathname}
            />
          ))}
        </nav>
        <LocaleSelector />
        <ThemeSelector />
        <GithubLink />
        {me ? (
          <UserMenu />
        ) : (
          <Link
            to="/login"
            className="text-sm font-medium text-muted-foreground hover:text-foreground transition-colors px-3 py-1.5"
          >
            {t("header.login")}
          </Link>
        )}

        {/* Mobile hamburger → Sheet */}
        <Sheet open={sheetOpen} onOpenChange={setSheetOpen}>
          <SheetTrigger
            className="sm:hidden p-1.5 rounded-md text-muted-foreground hover:text-foreground hover:bg-accent transition-colors ml-1"
            aria-label="Open navigation"
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
                <span className="font-serif italic text-xl tracking-tight select-none">stella</span>
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
              {utilNavItems.map((item) => (
                <MobileNavLink
                  key={item.href}
                  href={item.href}
                  label={item.label}
                  pathname={pathname}
                  onNavigate={() => setSheetOpen(false)}
                />
              ))}
            </nav>
          </SheetPopup>
        </Sheet>
      </div>
    </header>
  );
}

function ThemeSelector() {
  const [theme, setTheme] = useState<ThemeSettings>(() => getStoredTheme());

  useEffect(() => {
    if (theme.appearance !== "system") return;

    const media = window.matchMedia("(prefers-color-scheme: dark)");
    const update = () => applyTheme(theme);
    media.addEventListener("change", update);

    return () => media.removeEventListener("change", update);
  }, [theme]);

  function setAppearance(appearance: string) {
    if (!isAppearance(appearance)) return;
    const next = { appearance };
    setTheme(next);
    setStoredTheme(next);
  }

  const Icon = theme.appearance === "system" ? Monitor : theme.appearance === "light" ? Sun : Moon;

  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        className="inline-flex items-center gap-1 rounded-md p-1.5 text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
        aria-label={`Appearance: ${theme.appearance}`}
        title={`Appearance: ${theme.appearance}`}
      >
        <Icon className="size-4" />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" sideOffset={8} className="w-40">
        <DropdownMenuGroup>
          <DropdownMenuLabel>Appearance</DropdownMenuLabel>
          {THEME_APPEARANCES.map((appearance) => (
            <DropdownMenuItem
              key={appearance}
              onClick={() => setAppearance(appearance)}
              className="gap-2"
            >
              <ThemeCheck checked={theme.appearance === appearance} />
              {APPEARANCE_LABELS[appearance]}
            </DropdownMenuItem>
          ))}
        </DropdownMenuGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

const THEME_APPEARANCES: ThemeAppearance[] = ["system", "light", "dark"];

const APPEARANCE_LABELS: Record<ThemeAppearance, string> = {
  system: "System",
  light: "Light",
  dark: "Dark",
};

function isAppearance(value: string): value is ThemeAppearance {
  return value === "system" || value === "light" || value === "dark";
}

function ThemeCheck({ checked }: { checked: boolean }) {
  return (
    <span className="flex size-4 items-center justify-center text-foreground">
      {checked && <Check aria-hidden="true" className="size-4" />}
    </span>
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
  return (
    <Link
      to={href as never}
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
  return (
    <Link
      to={href as never}
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
  return (
    <a
      href="https://github.com/CherryHQ/stella"
      target="_blank"
      rel="noopener noreferrer"
      className="p-1.5 rounded-md text-muted-foreground hover:text-foreground hover:bg-accent transition-colors"
      aria-label="GitHub"
    >
      <svg viewBox="0 0 24 24" width="18" height="18" fill="currentColor">
        <path d="M12 0C5.37 0 0 5.37 0 12c0 5.31 3.435 9.795 8.205 11.385.6.105.825-.255.825-.57 0-.285-.015-1.23-.015-2.235-3.015.555-3.795-.735-4.035-1.41-.135-.345-.72-1.41-1.23-1.695-.42-.225-1.02-.78-.015-.795.945-.015 1.62.87 1.845 1.23 1.08 1.815 2.805 1.305 3.495.99.105-.78.42-1.305.765-1.605-2.67-.3-5.46-1.335-5.46-5.925 0-1.305.465-2.385 1.23-3.225-.12-.3-.54-1.53.12-3.18 0 0 1.005-.315 3.3 1.23.96-.27 1.98-.405 3-.405s2.04.135 3 .405c2.295-1.56 3.3-1.23 3.3-1.23.66 1.65.24 2.88.12 3.18.765.84 1.23 1.905 1.23 3.225 0 4.605-2.805 5.625-5.475 5.925.435.375.81 1.095.81 2.22 0 1.605-.015 2.895-.015 3.3 0 .315.225.69.825.57A12.02 12.02 0 0024 12c0-6.63-5.37-12-12-12z" />
      </svg>
    </a>
  );
}

function UserMenu() {
  const { data: me } = useQuery(meQueryOptions);
  const qc = useQueryClient();
  const navigate = useNavigate();
  const { t } = useI18n();

  if (!me) return null;

  async function logout() {
    await fetch("/api/auth/logout", { method: "POST", credentials: "same-origin" });
    qc.clear();
    void navigate({ to: "/login" });
  }

  return (
    <DropdownMenu>
      <DropdownMenuTrigger className="cursor-pointer flex items-center gap-2 px-2 py-1 rounded-lg hover:bg-accent text-sm font-medium transition-colors outline-none">
        <Avatar className="size-7">
          <AvatarFallback className="text-xs font-mono font-semibold">
            {me.username[0]?.toUpperCase()}
          </AvatarFallback>
        </Avatar>
        <span className="hidden md:inline text-sm text-muted-foreground">{me.username}</span>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" sideOffset={8} className="w-56">
        <DropdownMenuGroup>
          <DropdownMenuLabel>
            <div className="flex flex-col gap-0.5">
              <span className="text-sm font-medium text-foreground">{me.username}</span>
              {me.is_admin && <span className="text-xs text-muted-foreground">admin</span>}
            </div>
          </DropdownMenuLabel>
        </DropdownMenuGroup>
        <DropdownMenuSeparator />
        <DropdownMenuGroup>
          <DropdownMenuItem render={<Link to={"/sessions" as never} />}>
            {t("header.dashboard")}
          </DropdownMenuItem>
          <DropdownMenuItem render={<Link to={"/settings/account" as never} />}>
            {t("header.accountSettings")}
          </DropdownMenuItem>
        </DropdownMenuGroup>
        <DropdownMenuSeparator />
        <DropdownMenuItem variant="destructive" onClick={logout}>
          {t("header.logout")}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
