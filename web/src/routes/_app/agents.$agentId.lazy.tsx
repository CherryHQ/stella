import { createLazyFileRoute, Outlet, useParams, useRouterState } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { ConversationSidebar } from "@/features/sessions/ConversationSidebar";
import { AppBreadcrumb } from "@/features/sessions/AppBreadcrumb";
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

  const { data: agents = [] } = useQuery(agentsQueryOptions);
  const { data: projects = [] } = useQuery(agentProjectsOptions(agentId));
  const currentAgent = agents.find((agent) => agent.id === agentId);
  const currentProject = projects.find((project) => project.id === projectId);

  return (
    <AppShell
      sidebar={<ConversationSidebar />}
      title={
        <AppBreadcrumb
          agentId={agentId}
          agentName={currentAgent?.name ?? "Stella"}
          projectId={projectId || undefined}
          projectName={currentProject?.name}
        />
      }
    >
      <Outlet />
    </AppShell>
  );
}
