import { createLazyFileRoute } from "@tanstack/react-router";
import { AutomationEditPage } from "@/features/sessions/pages/AutomationEditPage";

export const Route = createLazyFileRoute("/_app/agents/$agentId/automations/$jobId/edit")({
  component: AutomationEditPage,
});
