import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link, useRouterState } from "@tanstack/react-router";
import { Bot, Folder, MessageSquare, Users } from "lucide-react";
import { useI18n } from "@/lib/i18n";
import { agentsQueryOptions } from "@/lib/queries/agents";
import { groupsQueryOptions } from "@/lib/queries/groups";
import { agentProjectsOptions } from "@/lib/queries/projects";
import { allChatSessionsQueryOptions } from "@/lib/queries/sessions";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogDescription,
  DialogHeader,
  DialogPanel,
  DialogPopup,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";

interface Result {
  key: string;
  icon: typeof Bot;
  label: string;
  to: string;
  params: Record<string, string>;
}

function match(haystack: string, needle: string): boolean {
  return haystack.toLowerCase().includes(needle);
}

/**
 * Global search over the conversation targets a user can reach: agents, groups,
 * the current agent's projects, and its full chat history.
 *
 * The sessions API has no server-side search and no cross-agent list, so chat
 * results are scoped to the agent in the URL — surfaced to the user as a hint
 * rather than silently dropped.
 */
export function GlobalSearchDialog({
  open,
  onOpenChange,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const { t } = useI18n();
  const [query, setQuery] = useState("");
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const agentId = pathname.match(/\/agents\/([^/]+)/)?.[1] ?? "";

  const { data: agents = [] } = useQuery(agentsQueryOptions);
  const { data: groups = [] } = useQuery(groupsQueryOptions);
  const { data: projects = [] } = useQuery(agentProjectsOptions(agentId));
  const { data: chats = [] } = useQuery(allChatSessionsQueryOptions(agentId, open));

  const needle = query.trim().toLowerCase();

  const sections = useMemo(() => {
    const limit = (items: Result[]) => (needle ? items.slice(0, 8) : items.slice(0, 5));
    const agentResults: Result[] = agents
      .filter((agent) => !needle || match(agent.name, needle))
      .map((agent) => ({
        key: `agent:${agent.id}`,
        icon: Bot,
        label: agent.name,
        to: "/agents/$agentId",
        params: { agentId: agent.id },
      }));
    const groupResults: Result[] = groups
      .filter((group) => !needle || match(group.group_name || "", needle))
      .map((group) => ({
        key: `group:${group.id}`,
        icon: Users,
        label: group.group_name || t("groups.unnamed"),
        to: "/groups/$groupId",
        params: { groupId: group.id },
      }));
    const projectResults: Result[] = projects
      .filter((project) => !needle || match(project.name, needle))
      .map((project) => ({
        key: `project:${project.id}`,
        icon: Folder,
        label: project.name,
        to: "/agents/$agentId/projects/$projectId",
        params: { agentId, projectId: project.id },
      }));
    const chatResults: Result[] = chats
      .filter((session) => !session.archived)
      .filter((session) => !needle || match(session.title || "", needle))
      .map((session) => ({
        key: `session:${session.id}`,
        icon: MessageSquare,
        label: session.title || t("sessions.untitled"),
        to: "/agents/$agentId/sessions/$sessionId",
        params: { agentId, sessionId: session.id },
      }));

    return [
      { key: "agents", label: t("search.agents"), items: limit(agentResults) },
      { key: "groups", label: t("search.groups"), items: limit(groupResults) },
      { key: "projects", label: t("search.projects"), items: limit(projectResults) },
      { key: "chats", label: t("search.chats"), items: limit(chatResults) },
    ].filter((section) => section.items.length > 0);
  }, [agentId, agents, chats, groups, needle, projects, t]);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogPopup showCloseButton={false} className="p-0">
        <DialogHeader className="sr-only">
          <DialogTitle>{t("search.open")}</DialogTitle>
          <DialogDescription>{t("search.placeholder")}</DialogDescription>
        </DialogHeader>
        <DialogPanel className="flex flex-col gap-3 p-3">
          <Input
            autoFocus
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder={t("search.placeholder")}
          />
          <div className="flex max-h-96 flex-col gap-3 overflow-y-auto">
            {sections.length === 0 ? (
              <p className="px-2 py-6 text-center text-sm text-muted-foreground">
                {t("search.empty")}
              </p>
            ) : (
              sections.map((section) => (
                <div key={section.key} className="flex flex-col gap-0.5">
                  <span className="px-2 py-1 text-xs text-muted-foreground">{section.label}</span>
                  {section.items.map((item) => {
                    const Icon = item.icon;
                    return (
                      <Button
                        key={item.key}
                        variant="ghost"
                        size="sm"
                        className="w-full justify-start"
                        onClick={() => onOpenChange(false)}
                        render={<Link to={item.to} params={item.params as never} />}
                      >
                        <Icon />
                        <span className="min-w-0 flex-1 truncate text-left">{item.label}</span>
                      </Button>
                    );
                  })}
                </div>
              ))
            )}
          </div>
          {agentId && (
            <p className="px-2 pb-1 text-xs text-muted-foreground">{t("search.scopeHint")}</p>
          )}
        </DialogPanel>
      </DialogPopup>
    </Dialog>
  );
}
