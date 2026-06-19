import { createLazyFileRoute } from "@tanstack/react-router";
import { GoalNewPage } from "@/features/goals/GoalNewPage";

export const Route = createLazyFileRoute("/_app/agents/$agentId/goals/new")({
  component: GoalNewPage,
});
