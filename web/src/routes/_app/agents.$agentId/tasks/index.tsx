import { createFileRoute } from "@tanstack/react-router";
import { TaskBoardPage } from "@/features/sessions/pages/TaskBoardPage";

export const Route = createFileRoute("/_app/agents/$agentId/tasks/")({
  component: TaskBoardPage,
});
