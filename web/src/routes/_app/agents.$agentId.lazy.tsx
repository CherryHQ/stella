import { createLazyFileRoute, Outlet, useParams, useRouterState } from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { AgentAppSidebar } from "@/features/sessions/AgentAppSidebar";
import { FacetTabs } from "@/features/sessions/FacetTabs";
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
  const queryClient = useQueryClient();

  const { data: agents = [] } = useQuery(agentsQueryOptions);
  const { data: projects = [] } = useQuery(agentProjectsOptions(agentId));
  const currentAgent = agents.find((agent) => agent.id === agentId);
  const currentProject = projects.find((project) => project.id === projectId);
  const title = currentProject?.name ?? currentAgent?.name ?? "Stella";

  const handleAgentChange = (newAgentId: string) => {
    if (newAgentId !== agentId) {
      void queryClient.invalidateQueries({ queryKey: ["sessions", newAgentId] });
    }
  };

  return (
    <AppShell
      sidebar={
        <AgentAppSidebar agents={agents} agentId={agentId} onAgentChange={handleAgentChange} />
      }
      subnav={
        <FacetTabs
          kind={projectId ? "project" : "agent"}
          agentId={agentId}
          projectId={projectId || undefined}
        />
      }
      title={<h1 className="truncate text-[15px] font-semibold tracking-[-0.01em]">{title}</h1>}
    >
      <Outlet />
    </AppShell>
  );
}
