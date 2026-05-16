import { createFileRoute } from "@tanstack/react-router";
import { TaskNewPage } from "@/features/sessions/pages/TaskNewPage";

export const Route = createFileRoute("/_app/agents/$agentId/tasks/new")({
  component: TaskNewPage,
});
