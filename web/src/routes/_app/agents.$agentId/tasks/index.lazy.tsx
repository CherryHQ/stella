import { createLazyFileRoute } from "@tanstack/react-router";
import { OverviewPage } from "@/features/tasks/OverviewPage";

export const Route = createLazyFileRoute("/_app/agents/$agentId/tasks/")({
  component: OverviewPage,
});
