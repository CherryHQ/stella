import { Link, useRouterState } from "@tanstack/react-router";
import { Brain, HardDrive, MessageSquare, Sparkles, Wrench } from "lucide-react";
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
  search?: Record<string, string>;
  icon: typeof MessageSquare;
  active: (pathname: string) => boolean;
  /* Facet exists in the IA but has no destination yet — rendered inert. */
  disabled?: boolean;
}

export function FacetTabs({ kind, agentId, groupId, projectId }: FacetTabsProps) {
  const { t } = useI18n();
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const searchTab = useRouterState({
    select: (s) => (s.location.search as { tab?: string }).tab,
  });
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
            disabled: true,
          },
          {
            key: "files",
            label: t("facets.files"),
            to: groupId ? `/groups/${groupId}` : "/agents",
            icon: HardDrive,
            active: () => false,
            disabled: true,
          },
        ]
      : kind === "project"
        ? [
            {
              key: "tasks",
              label: t("facets.tasks"),
              to: projectBase || "/agents",
              icon: Wrench,
              active: (p) =>
                (p === projectBase && (!searchTab || searchTab === "tasks")) ||
                p.startsWith(`${projectBase}/tasks`),
            },
            {
              key: "sessions",
              label: t("facets.sessions"),
              to: projectBase || "/agents",
              search: { tab: "sessions" },
              icon: MessageSquare,
              active: (p) =>
                (p === projectBase && searchTab === "sessions") ||
                p.startsWith(`${projectBase}/sessions`),
            },
            {
              key: "files",
              label: t("facets.files"),
              to: projectBase || "/agents",
              search: { tab: "files" },
              icon: HardDrive,
              active: (p) => p === projectBase && searchTab === "files",
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
              icon: Brain,
              active: (p) => p.startsWith(`${base}/memories`),
            },
            {
              key: "skills",
              label: t("facets.skills"),
              to: `${base}/skills`,
              icon: Sparkles,
              active: (p) => p.startsWith(`${base}/skills`),
            },
            {
              key: "files",
              label: t("facets.files"),
              to: base || "/agents",
              icon: HardDrive,
              active: () => false,
              disabled: true,
            },
          ];

  return (
    <nav className="flex h-11 shrink-0 items-center gap-1 overflow-x-auto border-b border-border bg-card/45 px-4 backdrop-blur-xl">
      {tabs.map((tab) => {
        const Icon = tab.icon;
        const active = tab.active(pathname);
        if (tab.disabled) {
          return (
            <span
              key={tab.key}
              aria-disabled
              title={t("facets.comingSoon")}
              className="flex h-8 shrink-0 cursor-not-allowed items-center gap-2 rounded-lg px-3 text-sm font-medium text-muted-foreground/50"
            >
              <Icon className="size-4" />
              <span>{tab.label}</span>
            </span>
          );
        }
        return (
          <Link
            key={tab.key}
            to={tab.to}
            search={tab.search}
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
