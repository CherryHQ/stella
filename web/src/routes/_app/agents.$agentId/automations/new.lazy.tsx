import { createLazyFileRoute } from "@tanstack/react-router";
import { AutomationNewPage } from "@/features/sessions/pages/AutomationNewPage";

export const Route = createLazyFileRoute("/_app/agents/$agentId/automations/new")({
  component: AutomationNewPage,
});
