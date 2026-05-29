import { createLazyFileRoute } from "@tanstack/react-router";
import { GoalDetailPage } from "@/features/automations/GoalDetailPage";

export const Route = createLazyFileRoute("/_app/agents/$agentId/automations/goals/$goalId")({
  component: GoalDetailPage,
});
