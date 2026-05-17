import { createLazyFileRoute } from "@tanstack/react-router";
import { TaskBoardPage } from "@/features/sessions/pages/TaskBoardPage";

export const Route = createLazyFileRoute("/_app/agents/$agentId/tasks/")({
  component: TaskBoardPage,
});
