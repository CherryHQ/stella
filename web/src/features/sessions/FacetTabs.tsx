import { Link, useRouterState } from "@tanstack/react-router";
import { Database, Library, ListTodo, MessageSquare, Puzzle } from "lucide-react";
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
        ]
      : kind === "project"
        ? [
            {
              key: "conversation",
              label: t("facets.conversation"),
              to: projectBase || "/agents",
              icon: MessageSquare,
              active: (p) =>
                (p === projectBase && (!searchTab || searchTab === "sessions")) ||
                p.startsWith(`${projectBase}/sessions`),
            },
            {
              key: "goals",
              label: t("facets.goals"),
              to: projectBase || "/agents",
              search: { tab: "goals" },
              icon: ListTodo,
              active: (p) => p === projectBase && searchTab === "goals",
            },
            {
              key: "memory",
              label: t("facets.memory"),
              to: `${projectBase}/memories`,
              icon: Database,
              active: (p) => p.startsWith(`${projectBase}/memories`),
            },
            {
              key: "skills",
              label: t("facets.skills"),
              to: `${projectBase}/skills`,
              icon: Puzzle,
              active: (p) => p.startsWith(`${projectBase}/skills`),
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
              key: "goals",
              label: t("facets.goals"),
              to: `${base}/goals`,
              icon: ListTodo,
              // Workflow pages are part of the Goals surface (no tab of their own).
              active: (p) => p.startsWith(`${base}/goals`) || p.startsWith(`${base}/workflows`),
            },
            {
              key: "memory",
              label: t("facets.memory"),
              to: `${base}/memories`,
              icon: Database,
              active: (p) => p.startsWith(`${base}/memories`),
            },
            {
              key: "knowledge",
              label: t("facets.knowledge"),
              to: `${base}/knowledge`,
              icon: Library,
              active: (p) => p.startsWith(`${base}/knowledge`),
            },
            {
              key: "skills",
              label: t("facets.skills"),
              to: `${base}/skills`,
              icon: Puzzle,
              active: (p) => p.startsWith(`${base}/skills`),
            },
          ];

  return (
    <nav className="flex h-8 min-w-0 items-center gap-1 overflow-x-auto">
      {tabs.map((tab) => {
        const Icon = tab.icon;
        const active = tab.active(pathname);
        if (tab.disabled) {
          return (
            <span
              key={tab.key}
              aria-disabled
              title={t("facets.comingSoon")}
              className="flex h-9 shrink-0 cursor-not-allowed items-center gap-2 rounded-md px-3 text-sm font-medium text-muted-foreground/50 sm:h-8"
            >
              <Icon className="size-4" />
              <span className="hidden sm:inline">{tab.label}</span>
            </span>
          );
        }
        return (
          <Link
            key={tab.key}
            to={tab.to}
            search={tab.search}
            className={cn(
              "flex h-9 shrink-0 items-center gap-2 rounded-md px-3 text-sm font-medium transition-colors sm:h-8",
              active
                ? "bg-accent text-accent-foreground"
                : "text-muted-foreground hover:bg-muted/50 hover:text-foreground",
            )}
          >
            <Icon className="size-4" />
            <span className="hidden sm:inline">{tab.label}</span>
          </Link>
        );
      })}
    </nav>
  );
}
