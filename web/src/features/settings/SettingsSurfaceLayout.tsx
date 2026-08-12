import type { LucideIcon } from "lucide-react";
import { Outlet, useRouterState } from "@tanstack/react-router";
import { SidebarItem, SidebarSection } from "@/components/AppSidebar";
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

export function findActiveSettingsNavItem(groups: SettingsNavGroup[], pathname: string) {
  return groups
    .flatMap((group) => group.items)
    .sort((a, b) => b.href.length - a.href.length)
    .find((item) => pathname === item.href || pathname.startsWith(`${item.href}/`));
}

function SurfaceNav({ groups, activeHref }: { groups: SettingsNavGroup[]; activeHref?: string }) {
  const { t } = useI18n();
  const { setOpenMobile } = useSidebar();

  return (
    <div className="flex flex-col px-3 pb-2">
      {groups.map((group) => (
        <SidebarSection key={group.label} title={t(group.label)}>
          {group.items.map((item) => {
            const Icon = item.icon;
            return (
              <SidebarItem
                key={item.href}
                active={activeHref === item.href}
                icon={<Icon aria-hidden="true" size={16} />}
                label={t(item.label)}
                to={item.href}
                onClick={() => setOpenMobile(false)}
              />
            );
          })}
        </SidebarSection>
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
  const activeItem = findActiveSettingsNavItem(groups, pathname);

  return (
    <AppShell
      sidebar={<SurfaceNav groups={groups} activeHref={activeItem?.href} />}
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
