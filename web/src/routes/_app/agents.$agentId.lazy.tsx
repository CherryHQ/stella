import {
  createLazyFileRoute,
  Outlet,
  useNavigate,
  useParams,
  useRouterState,
} from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { AgentSidebarContent } from "@/features/sessions/AgentSidebar";
import { agentsQueryOptions } from "@/lib/queries/agents";
import { AppShell } from "@/layouts/AppShell";

export const Route = createLazyFileRoute("/_app/agents/$agentId")({
  component: AgentLayout,
});

function AgentLayout() {
  const { agentId } = useParams({ from: "/_app/agents/$agentId" });
  const navigate = useNavigate();
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const queryClient = useQueryClient();

  const { data: agents = [] } = useQuery(agentsQueryOptions);

  const handleAgentChange = (newAgentId: string) => {
    if (newAgentId === agentId) return;
    void queryClient.invalidateQueries({ queryKey: ["sessions", newAgentId] });
    void navigate({ to: "/agents/$agentId", params: { agentId: newAgentId } });
  };

  return (
    <AppShell
      sidebar={
        <AgentSidebarContent
          agents={agents}
          agentId={agentId}
          pathname={pathname}
          onAgentChange={handleAgentChange}
        />
      }
    >
      <Outlet />
    </AppShell>
  );
}
