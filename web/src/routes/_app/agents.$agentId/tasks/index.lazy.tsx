import { createLazyFileRoute } from "@tanstack/react-router";
import { OverviewPage } from "@/features/automations/OverviewPage";

export const Route = createLazyFileRoute("/_app/agents/$agentId/tasks/")({
  component: OverviewPage,
});
