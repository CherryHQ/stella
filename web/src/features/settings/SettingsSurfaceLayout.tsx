import type { LucideIcon } from "lucide-react";
import { Link, Outlet, useRouterState } from "@tanstack/react-router";
import { useSidebar } from "@/components/ui/sidebar";
import { AppShell } from "@/layouts/AppShell";
import { useI18n } from "@/lib/i18n";
import type { MessageKey } from "@/lib/i18n/messages";

export interface SettingsNavItem {
  label: MessageKey;
  href: string;
  icon: LucideIcon;
}

export interface SettingsNavGroup {
  label: MessageKey;
  items: SettingsNavItem[];
}

function SurfaceNav({ groups }: { groups: SettingsNavGroup[] }) {
  const { t } = useI18n();
  const { isMobile, setOpenMobile } = useSidebar();

  return (
    <div className="flex flex-col gap-4 bg-sidebar px-3 pb-2">
      {groups.map((group) => (
        <div key={group.label} className="flex flex-col gap-0.5">
          <div className="px-2 pt-2 text-xs font-semibold text-muted-foreground">
            {t(group.label)}
          </div>
          {group.items.map((item) => {
            const Icon = item.icon;
            return (
              <Link
                key={item.href}
                to={item.href as never}
                onClick={() => {
                  if (isMobile) setOpenMobile(false);
                }}
                className="flex min-h-9 items-center gap-2 rounded-lg px-2.5 py-2 text-sm font-medium text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
                activeProps={{
                  className:
                    "flex min-h-9 items-center gap-2 rounded-lg bg-accent px-2.5 py-2 text-sm font-semibold text-accent-foreground",
                }}
              >
                <Icon aria-hidden="true" />
                {t(item.label)}
              </Link>
            );
          })}
        </div>
      ))}
    </div>
  );
}

export function SettingsSurfaceLayout({
  title,
  groups,
}: {
  title: MessageKey;
  groups: SettingsNavGroup[];
}) {
  const { t } = useI18n();
  const pathname = useRouterState({ select: (state) => state.location.pathname });
  const activeItem = groups
    .flatMap((group) => group.items)
    .sort((a, b) => b.href.length - a.href.length)
    .find((item) => pathname === item.href || pathname.startsWith(`${item.href}/`));

  return (
    <AppShell
      sidebar={<SurfaceNav groups={groups} />}
      title={
        <span className="text-sm font-semibold">
          {t(title)}
          {activeItem ? ` / ${t(activeItem.label)}` : ""}
        </span>
      }
    >
      <div className="flex h-full flex-col overflow-hidden bg-background">
        <Outlet />
      </div>
    </AppShell>
  );
}
