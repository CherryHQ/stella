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
import { cn } from "@/lib/utils";
import { AgentLayoutContext } from "@/features/sessions/AgentLayoutContext";
import { Sheet, SheetPopup, SheetTitle, SheetDescription } from "@/components/ui/sheet";

export const Route = createLazyFileRoute("/_app/agents/$agentId")({
  component: AgentLayout,
});

function AgentLayout() {
  const { agentId } = useParams({ from: "/_app/agents/$agentId" });
  const navigate = useNavigate();
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const queryClient = useQueryClient();
  const [sidebarOpen, setSidebarOpen] = useState(true);
  const [mobileSidebarOpen, setMobileSidebarOpen] = useState(false);

  const { data: agents = [] } = useQuery(agentsQueryOptions);

  const handleAgentChange = (newAgentId: string) => {
    if (newAgentId === agentId) return;
    void queryClient.invalidateQueries({ queryKey: ["sessions", newAgentId] });
    void navigate({ to: "/agents/$agentId", params: { agentId: newAgentId } });
  };

  const openMobileSidebar = useCallback(() => setMobileSidebarOpen(true), []);

  return (
    <AgentLayoutContext.Provider
      value={{
        sidebarOpen,
        toggleSidebar: () => setSidebarOpen((o) => !o),
        openMobileSidebar,
      }}
    >
      <div className="flex h-full min-h-0 overflow-hidden">
        <AgentSidebar
          agents={agents}
          agentId={agentId}
          pathname={pathname}
          onAgentChange={handleAgentChange}
          className={cn(
            "hidden transition-[width,opacity] duration-200 ease-out md:flex",
            sidebarOpen ? "md:w-[260px]" : "md:w-0 border-r-0 opacity-0 pointer-events-none",
          )}
        />
        <div className="flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden">
          <Outlet />
        </div>
      </div>

      <Sheet open={mobileSidebarOpen} onOpenChange={setMobileSidebarOpen}>
        <SheetPopup side="left" showCloseButton={false} className="w-[280px] md:hidden">
          <SheetTitle className="sr-only">Navigation</SheetTitle>
          <SheetDescription className="sr-only">Agent navigation sidebar</SheetDescription>
          <AgentSidebar
            agents={agents}
            agentId={agentId}
            pathname={pathname}
            onAgentChange={handleAgentChange}
            onCloseMobile={() => setMobileSidebarOpen(false)}
            className="w-full border-r-0"
          />
        </SheetPopup>
      </Sheet>
    </AgentLayoutContext.Provider>
  );
}
