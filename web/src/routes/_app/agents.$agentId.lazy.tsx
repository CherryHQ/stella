import { createLazyFileRoute, Outlet, useParams, useRouterState } from "@tanstack/react-router";
import { useEffect } from "react";
import { useQuery } from "@tanstack/react-query";
import { writeLastAgentId } from "@/lib/last-agent";
import { ConversationSidebar } from "@/features/sessions/ConversationSidebar";
import { AppBreadcrumb } from "@/features/sessions/AppBreadcrumb";
import { agentsQueryOptions } from "@/lib/queries/agents";
import { agentProjectsOptions } from "@/lib/queries/projects";
import { AppShell } from "@/layouts/AppShell";

export const Route = createLazyFileRoute("/_app/agents/$agentId")({
  component: AgentLayout,
});

function AgentLayout() {
  const { agentId } = useParams({ from: "/_app/agents/$agentId" });
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const projectId = pathname.match(/\/projects\/([^/]+)/)?.[1] ?? "";

  // Every agent page mounts through this layout, so it is the one place that
  // sees "the agent you are working with" without extra bookkeeping. The home
  // composer reads it back to preselect an agent.
  useEffect(() => writeLastAgentId(agentId), [agentId]);

  const { data: agents = [] } = useQuery(agentsQueryOptions);
  const { data: projects = [] } = useQuery(agentProjectsOptions(agentId));
  const currentAgent = agents.find((agent) => agent.id === agentId);
  const currentProject = projects.find((project) => project.id === projectId);

  return (
    <AppShell
      sidebar={<ConversationSidebar />}
      title={
        <AppBreadcrumb
          agentId={agentId}
          agentName={currentAgent?.name ?? "Stella"}
          projectId={projectId || undefined}
          projectName={currentProject?.name}
        />
      }
    >
      <Outlet />
    </AppShell>
  );
}
