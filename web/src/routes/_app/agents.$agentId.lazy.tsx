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
import { Sidebar, SidebarInset, SidebarProvider, SidebarTrigger } from "@/components/ui/sidebar";

export const Route = createLazyFileRoute("/_app/agents/$agentId")({
  component: AgentLayout,
});

function AgentLayout() {
  const { agentId } = useParams({ from: "/_app/agents/$agentId" });
  const navigate = useNavigate();
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const queryClient = useQueryClient();

  const { data: agents = [] } = useQuery(agentsQueryOptions);
  const currentAgent = agents.find((a) => a.id === agentId);

  const handleAgentChange = (newAgentId: string) => {
    if (newAgentId === agentId) return;
    void queryClient.invalidateQueries({ queryKey: ["sessions", newAgentId] });
    void navigate({ to: "/agents/$agentId", params: { agentId: newAgentId } });
  };

  return (
    <SidebarProvider
      className="h-full min-h-0"
      style={{ "--sidebar-width": "260px" } as React.CSSProperties}
    >
      <Sidebar>
        <AgentSidebar
          agents={agents}
          agentId={agentId}
          pathname={pathname}
          onAgentChange={handleAgentChange}
        />
      </Sidebar>

      <SidebarInset className="flex flex-col overflow-hidden">
        <header className="flex h-12 shrink-0 items-center gap-3 border-b border-border bg-card/85 px-4 md:hidden">
          <SidebarTrigger />
          {currentAgent && <span className="text-sm font-semibold">{currentAgent.name}</span>}
        </header>
        <div className="flex min-h-0 flex-1 flex-col">
          <Outlet />
        </div>
      </SidebarInset>
    </SidebarProvider>
  );
}
