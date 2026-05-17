import { createLazyFileRoute } from "@tanstack/react-router";
import { TaskNewPage } from "@/features/sessions/pages/TaskNewPage";

export const Route = createLazyFileRoute("/_app/agents/$agentId/tasks/new")({
  component: TaskNewPage,
});
