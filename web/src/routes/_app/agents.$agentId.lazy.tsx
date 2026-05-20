import { useCallback, useEffect, useState } from "react";
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

const LEFT_COLLAPSED_KEY = "stella-left-collapsed";

function getCollapsed(): boolean {
  return (
    document.querySelector("[data-left-collapsed]")?.getAttribute("data-left-collapsed") === "true"
  );
}

function setCollapsedDOM(value: boolean) {
  const el = document.querySelector("[data-left-collapsed]");
  if (el) el.setAttribute("data-left-collapsed", value ? "true" : "false");
  localStorage.setItem(LEFT_COLLAPSED_KEY, value ? "1" : "0");
}

export const Route = createLazyFileRoute("/_app/agents/$agentId")({
  component: AgentLayout,
});

function AgentLayout() {
  const { agentId } = useParams({ from: "/_app/agents/$agentId" });
  const navigate = useNavigate();
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const queryClient = useQueryClient();

  const { data: agents = [] } = useQuery(agentsQueryOptions);

  const [collapsed, setCollapsed] = useState(false);

  useEffect(() => {
    setCollapsed(getCollapsed());
    const target = document.querySelector("[data-left-collapsed]");
    if (!target) return;
    const obs = new MutationObserver(() => setCollapsed(getCollapsed()));
    obs.observe(target, { attributes: true, attributeFilter: ["data-left-collapsed"] });
    return () => obs.disconnect();
  }, []);

  const toggle = useCallback(() => {
    const next = !getCollapsed();
    setCollapsedDOM(next);
    setCollapsed(next);
  }, []);

  const handleAgentChange = (newAgentId: string) => {
    if (newAgentId === agentId) return;
    void queryClient.invalidateQueries({ queryKey: ["sessions", newAgentId] });
    void navigate({ to: "/agents/$agentId", params: { agentId: newAgentId } });
  };

  return (
    <div className="relative flex h-full overflow-hidden">
      <div className="stella-left-panel w-[260px] min-w-[260px] flex-shrink-0 border-r border-border/60 bg-sidebar transition-[width,min-width,opacity] duration-200 ease-out">
        <AgentSidebar
          agents={agents}
          agentId={agentId}
          pathname={pathname}
          onAgentChange={handleAgentChange}
        />
      </div>

      <button
        type="button"
        onClick={toggle}
        className={cn(
          "absolute top-3 z-20 hidden h-[34px] w-[18px] place-items-center rounded-full border border-border/60 bg-card text-muted-foreground/50 shadow-sm transition-all duration-200 hover:bg-accent hover:text-foreground md:grid",
          collapsed ? "left-1" : "left-[250px]",
        )}
        aria-label={collapsed ? "Show sidebar" : "Hide sidebar"}
      >
        <svg
          className={cn("size-3 transition-transform", collapsed && "rotate-180")}
          viewBox="0 0 16 16"
          fill="none"
          stroke="currentColor"
          strokeWidth="2"
        >
          <path d="m10 4-4 4 4 4" />
        </svg>
      </button>

      <div className="flex-1 min-w-0 flex flex-col overflow-hidden">
        <Outlet />
      </div>
    </div>
  );
}
