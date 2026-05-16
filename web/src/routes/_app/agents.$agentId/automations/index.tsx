import { createFileRoute } from "@tanstack/react-router";
import { AutomationDashPage } from "@/features/sessions/pages/AutomationDashPage";

export const Route = createFileRoute("/_app/agents/$agentId/automations/")({
  component: AutomationDashPage,
});
