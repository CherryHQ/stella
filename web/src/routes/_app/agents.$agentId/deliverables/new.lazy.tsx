import { createLazyFileRoute } from "@tanstack/react-router";
import { DeliverableNewPage } from "@/features/deliverables/DeliverableNewPage";

export const Route = createLazyFileRoute("/_app/agents/$agentId/deliverables/new")({
  component: DeliverableNewPage,
});
