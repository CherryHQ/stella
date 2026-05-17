import {
  createLazyFileRoute,
  Outlet,
  useNavigate,
  useParams,
  useRouterState,
} from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { AgentSidebar } from "@/features/sessions/AgentSidebar";
import { agentsQueryOptions } from "@/lib/queries/agents";

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
    <div className="flex overflow-hidden" style={{ height: "calc(100vh - 3.5rem)" }}>
      <div className="w-[260px] min-w-[260px] flex-shrink-0 border-r border-border/60 bg-sidebar">
        <AgentSidebar
          agents={agents}
          agentId={agentId}
          pathname={pathname}
          onAgentChange={handleAgentChange}
        />
      </div>
      <div className="flex-1 min-w-0 flex flex-col overflow-hidden">
        <Outlet />
      </div>
    </div>
  );
}
