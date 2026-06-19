import { createLazyFileRoute } from "@tanstack/react-router";
import { OverviewPage } from "@/features/deliverables/OverviewPage";

export const Route = createLazyFileRoute("/_app/agents/$agentId/deliverables/")({
  component: OverviewPage,
});
