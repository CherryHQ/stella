import { createLazyFileRoute } from "@tanstack/react-router";
import { GoalsPage } from "@/features/automations/GoalsPage";

export const Route = createLazyFileRoute("/_app/agents/$agentId/automations/")({
  component: GoalsPage,
});
