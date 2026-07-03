import { createLazyFileRoute } from "@tanstack/react-router";
import { WorkflowsPage } from "@/features/workflows/WorkflowsPage";

export const Route = createLazyFileRoute("/_app/agents/$agentId/workflows")({
  component: WorkflowsPage,
});
