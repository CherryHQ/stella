import { createFileRoute } from "@tanstack/react-router";
import { AutomationEditPage } from "@/features/sessions/pages/AutomationEditPage";

export const Route = createFileRoute("/_app/agents/$agentId/automations/$jobId")({
  component: AutomationEditPage,
});
