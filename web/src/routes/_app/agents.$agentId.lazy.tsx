import { useState } from "react";
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
import { cn } from "@/lib/utils";
import { AgentLayoutContext } from "@/features/sessions/AgentLayoutContext";

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

  return (
    <AgentLayoutContext.Provider
      value={{ sidebarOpen, toggleSidebar: () => setSidebarOpen((o) => !o) }}
    >
      <div className="flex h-full min-h-0 overflow-hidden">
        <AgentSidebar
          agents={agents}
          agentId={agentId}
          pathname={pathname}
          onAgentChange={handleAgentChange}
          className={cn(
            "transition-[width,opacity] duration-200 ease-out",
            sidebarOpen ? "w-[260px]" : "w-0 border-r-0 opacity-0 pointer-events-none",
          )}
        />
        <div className="flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden">
          <Outlet />
        </div>
      </div>
    </AgentLayoutContext.Provider>
  );
}
