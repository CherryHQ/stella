import {
  createFileRoute,
  Outlet,
  useNavigate,
  useParams,
  useRouterState,
} from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  agentsQueryOptions,
  agentSchedulerJobsOptions,
  agentSkillsOptions,
  agentMemoriesOptions,
} from "@/lib/queries/agents";
import { AgentSidebar } from "@/features/sessions/AgentSidebar";

export const Route = createFileRoute("/_app/agents/$agentId")({
  loader: async ({ context: { queryClient }, params: { agentId } }) => {
    await Promise.all([
      queryClient.ensureQueryData(agentsQueryOptions),
      queryClient.ensureQueryData(agentSchedulerJobsOptions(agentId)),
      queryClient.ensureQueryData(agentSkillsOptions(agentId)),
      queryClient.ensureQueryData(agentMemoriesOptions(agentId)),
    ]);
  },
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
