import { createFileRoute } from "@tanstack/react-router";
import { AutomationNewPage } from "@/features/sessions/pages/AutomationNewPage";

export const Route = createFileRoute("/_app/agents/$agentId/automations/new")({
  component: AutomationNewPage,
});
