import { Link, useRouterState } from "@tanstack/react-router";
import { Bot, HardDrive, MessageSquare, Users, Wrench } from "lucide-react";
import { useI18n } from "@/lib/i18n";
import { cn } from "@/lib/utils";

type FacetKind = "agent" | "group" | "project";

interface FacetTabsProps {
  kind: FacetKind;
  agentId?: string;
  groupId?: string;
  projectId?: string;
}

interface TabDef {
  key: string;
  label: string;
  to: string;
  icon: typeof MessageSquare;
  active: (pathname: string) => boolean;
}

export function FacetTabs({ kind, agentId, groupId, projectId }: FacetTabsProps) {
  const { t } = useI18n();
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const base = agentId ? `/agents/${agentId}` : "";
  const projectBase = agentId && projectId ? `/agents/${agentId}/projects/${projectId}` : "";

  const tabs: TabDef[] =
    kind === "group"
      ? [
          {
            key: "conversation",
            label: t("facets.conversation"),
            to: groupId ? `/groups/${groupId}` : "/agents",
            icon: MessageSquare,
            active: (p) => p.startsWith(`/groups/${groupId}`),
          },
          {
            key: "tasks",
            label: t("facets.tasks"),
            to: groupId ? `/groups/${groupId}` : "/agents",
            icon: Wrench,
            active: () => false,
          },
          {
            key: "files",
            label: t("facets.files"),
            to: groupId ? `/groups/${groupId}` : "/agents",
            icon: HardDrive,
            active: () => false,
          },
        ]
      : kind === "project"
        ? [
            {
              key: "tasks",
              label: t("facets.tasks"),
              to: projectBase || "/agents",
              icon: Wrench,
              active: (p) => p === projectBase || p.startsWith(`${projectBase}/tasks`),
            },
            {
              key: "conversation",
              label: t("facets.conversation"),
              to: projectBase || "/agents",
              icon: MessageSquare,
              active: (p) => p.startsWith(`${projectBase}/sessions`),
            },
            {
              key: "files",
              label: t("facets.files"),
              to: projectBase || "/agents",
              icon: HardDrive,
              active: () => false,
            },
          ]
        : [
            {
              key: "conversation",
              label: t("facets.conversation"),
              to: base || "/agents",
              icon: MessageSquare,
              active: (p) => p === base || p.startsWith(`${base}/sessions`),
            },
            {
              key: "tasks",
              label: t("facets.tasks"),
              to: `${base}/automations`,
              icon: Wrench,
              active: (p) => p.startsWith(`${base}/automations`) || p.startsWith(`${base}/tasks`),
            },
            {
              key: "memory",
              label: t("facets.memory"),
              to: `${base}/memories`,
              icon: Bot,
              active: (p) => p.startsWith(`${base}/memories`),
            },
            {
              key: "skills",
              label: t("facets.skills"),
              to: `${base}/skills`,
              icon: Users,
              active: (p) => p.startsWith(`${base}/skills`),
            },
            {
              key: "files",
              label: t("facets.files"),
              to: base || "/agents",
              icon: HardDrive,
              active: () => false,
            },
          ];

  return (
    <nav className="flex h-11 shrink-0 items-center gap-1 overflow-x-auto border-b border-border bg-card/45 px-4 backdrop-blur-xl">
      {tabs.map((tab) => {
        const Icon = tab.icon;
        const active = tab.active(pathname);
        return (
          <Link
            key={tab.key}
            to={tab.to}
            className={cn(
              "flex h-8 shrink-0 items-center gap-2 rounded-lg px-3 text-sm font-medium transition-colors",
              active
                ? "bg-muted text-foreground"
                : "text-muted-foreground hover:bg-muted/50 hover:text-foreground",
            )}
          >
            <Icon className="size-4" />
            <span>{tab.label}</span>
          </Link>
        );
      })}
    </nav>
  );
}
