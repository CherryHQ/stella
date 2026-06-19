import { createLazyFileRoute } from "@tanstack/react-router";
import { GoalsPage } from "@/features/goals/GoalsPage";

export const Route = createLazyFileRoute("/_app/agents/$agentId/goals/all")({
  component: GoalsPage,
});
