import { createLazyFileRoute } from "@tanstack/react-router";
import { ProjectHome } from "@/features/sessions/ProjectHome";

export const Route = createLazyFileRoute("/_app/agents/$agentId/projects/$projectId/")({
  component: ProjectHome,
});
