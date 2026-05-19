import { useCallback, useState } from "react";
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
import { SidebarToggleContext } from "@/hooks/use-sidebar-toggle";
import { cn } from "@/lib/utils";

export const Route = createLazyFileRoute("/_app/agents/$agentId")({
  component: AgentLayout,
});

function AgentLayout() {
  const { agentId } = useParams({ from: "/_app/agents/$agentId" });
  const navigate = useNavigate();
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const queryClient = useQueryClient();
  const [sidebarOpen, setSidebarOpen] = useState(true);

  const { data: agents = [] } = useQuery(agentsQueryOptions);

  const handleAgentChange = (newAgentId: string) => {
    if (newAgentId === agentId) return;
    void queryClient.invalidateQueries({ queryKey: ["sessions", newAgentId] });
    void navigate({ to: "/agents/$agentId", params: { agentId: newAgentId } });
  };

  const toggleSidebar = useCallback(() => setSidebarOpen((v) => !v), []);

  return (
    <SidebarToggleContext value={{ sidebarOpen, toggleSidebar }}>
      <div className="flex overflow-hidden" style={{ height: "calc(100vh - 3.5rem)" }}>
        <div
          className={cn(
            "flex-shrink-0 border-r border-border/60 bg-sidebar transition-[width,min-width,opacity] duration-200 ease-out overflow-hidden",
            sidebarOpen ? "w-[280px] min-w-[280px]" : "w-0 min-w-0 opacity-0 pointer-events-none",
          )}
        >
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
    </SidebarToggleContext>
  );
}
