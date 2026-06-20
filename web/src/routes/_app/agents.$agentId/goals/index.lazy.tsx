import { createLazyFileRoute } from "@tanstack/react-router";
import { OverviewPage } from "@/features/goals/OverviewPage";

export const Route = createLazyFileRoute("/_app/agents/$agentId/goals/")({
  component: OverviewPage,
});
