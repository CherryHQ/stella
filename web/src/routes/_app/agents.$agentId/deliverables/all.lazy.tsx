import { createLazyFileRoute } from "@tanstack/react-router";
import { DeliverablesPage } from "@/features/deliverables/DeliverablesPage";

export const Route = createLazyFileRoute("/_app/agents/$agentId/deliverables/all")({
  component: DeliverablesPage,
});
