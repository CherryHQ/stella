import { useQuery } from "@tanstack/react-query";
import {
  Bot,
  CircleUserRound,
  Info,
  KeyRound,
  Library,
  MessageSquare,
  Puzzle,
  Webhook,
  Wrench,
} from "lucide-react";
import { meQueryOptions } from "@/lib/queries/me";
import type { SettingsNavGroup } from "@/features/settings/SettingsSurfaceLayout";
import { SettingsSurfaceLayout } from "@/features/settings/SettingsSurfaceLayout";

const personalNav: SettingsNavGroup[] = [
  {
    label: "settings.section.resources",
    items: [
      { label: "settings.nav.agents", href: "/settings/agents", icon: Bot },
      { label: "settings.nav.channels", href: "/settings/channels", icon: MessageSquare },
      { label: "settings.nav.webhooks", href: "/settings/webhooks", icon: Webhook },
      { label: "settings.nav.credentials", href: "/settings/credentials", icon: KeyRound },
      { label: "settings.nav.library", href: "/settings/library", icon: Library },
      { label: "settings.nav.skills", href: "/settings/skills", icon: Puzzle },
    ],
  },
  {
    label: "settings.section.account",
    items: [{ label: "settings.nav.account", href: "/settings/account", icon: CircleUserRound }],
  },
];

const personalUserOnlyNav: SettingsNavGroup = {
  label: "settings.section.resources",
  items: [{ label: "mcp.title", href: "/settings/plugins", icon: Wrench }],
};

const aboutNav: SettingsNavGroup = {
  label: "settings.section.about",
  items: [{ label: "settings.nav.about", href: "/settings/about", icon: Info }],
};

export function SettingsLayout() {
  const { data: me } = useQuery(meQueryOptions);
  const groups = me?.is_admin
    ? personalNav
    : [
        {
          ...personalNav[0],
          items: [...personalNav[0].items, ...personalUserOnlyNav.items],
        },
        personalNav[1],
        aboutNav,
      ];

  return <SettingsSurfaceLayout title="nav.personalSettings" groups={groups} />;
}
