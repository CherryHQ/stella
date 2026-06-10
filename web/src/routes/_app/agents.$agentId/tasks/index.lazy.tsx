import { createLazyFileRoute } from "@tanstack/react-router";
import { AutomationsPage } from "@/features/automations/AutomationsPage";

export const Route = createLazyFileRoute("/_app/agents/$agentId/tasks/")({
  component: AutomationsPage,
});
