import { useQuery } from "@tanstack/react-query";
import {
  Blocks,
  Bot,
  Gauge,
  KeyRound,
  Library,
  Puzzle,
  Sparkles,
  Users,
  Wrench,
  ShieldCheck,
} from "lucide-react";
import { ErrorState } from "@/components/RouteFallback";
import { AppShell } from "@/layouts/AppShell";
import { meQueryOptions } from "@/lib/queries/me";
import { useI18n } from "@/lib/i18n";
import type { SettingsNavGroup } from "@/features/settings/SettingsSurfaceLayout";
import { SettingsSurfaceLayout } from "@/features/settings/SettingsSurfaceLayout";

export const adminSettingsNav: SettingsNavGroup[] = [
  {
    label: "admin.section.operations",
    items: [{ label: "admin.nav.overview", href: "/admin/overview", icon: Gauge }],
  },
  {
    label: "admin.section.access",
    items: [
      { label: "settings.nav.users", href: "/admin/users", icon: Users },
      {
        label: "settings.nav.provisioning",
        href: "/admin/access/provisioning",
        icon: KeyRound,
      },
    ],
  },
  {
    label: "admin.section.ai",
    items: [
      {
        label: "settings.nav.providers",
        href: "/admin/ai/providers",
        icon: Bot,
      },
      {
        label: "settings.nav.defaultModels",
        href: "/admin/ai/models",
        icon: Sparkles,
      },
    ],
  },
  {
    label: "admin.section.resources",
    items: [
      {
        label: "admin.resources.skills.title",
        href: "/admin/resources/skills",
        icon: Puzzle,
      },
      {
        label: "admin.resources.mcp.title",
        href: "/admin/resources/mcp",
        icon: Wrench,
      },
      {
        label: "admin.resources.credentials.title",
        href: "/admin/resources/credentials",
        icon: KeyRound,
      },
      {
        label: "admin.resources.library.title",
        href: "/admin/resources/library",
        icon: Library,
      },
    ],
  },
  {
    label: "admin.section.integrations",
    items: [
      {
        label: "settings.nav.plugins",
        href: "/admin/integrations/plugins",
        icon: Blocks,
      },
      {
        label: "settings.nav.nativePlugins",
        href: "/admin/integrations/native",
        icon: ShieldCheck,
      },
    ],
  },
];

export function AdminLayout() {
  const { data: me } = useQuery(meQueryOptions);
  const { t } = useI18n();

  if (!me?.is_admin) {
    return (
      <AppShell
        sidebar={null}
        title={<span className="text-sm font-semibold">{t("nav.adminConsole")}</span>}
      >
        <ErrorState title={t("admin.forbidden.title")} description={t("admin.forbidden.desc")} />
      </AppShell>
    );
  }

  return <SettingsSurfaceLayout title="nav.adminConsole" groups={adminSettingsNav} />;
}
