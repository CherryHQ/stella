import {
  createLazyFileRoute,
  Outlet,
  useNavigate,
  useParams,
  useRouterState,
} from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { AgentAppSidebar } from "@/features/sessions/AgentAppSidebar";
import { FacetTabs } from "@/features/sessions/FacetTabs";
import { agentsQueryOptions } from "@/lib/queries/agents";
import { AppShell } from "@/layouts/AppShell";

export const Route = createLazyFileRoute("/_app/agents/$agentId")({
  component: AgentLayout,
});

function AgentLayout() {
  const { agentId } = useParams({ from: "/_app/agents/$agentId" });
  const navigate = useNavigate();
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const projectId = pathname.match(/\/projects\/([^/]+)/)?.[1] ?? "";
  const queryClient = useQueryClient();

  const { data: agents = [] } = useQuery(agentsQueryOptions);

  const handleAgentChange = (newAgentId: string) => {
    if (newAgentId !== agentId) {
      void queryClient.invalidateQueries({ queryKey: ["sessions", newAgentId] });
    }
    void navigate({ to: "/agents/$agentId", params: { agentId: newAgentId } });
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
    >
      <Outlet />
    </AppShell>
  );
}
