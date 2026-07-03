import { createLazyFileRoute } from "@tanstack/react-router";
import { WorkflowDetailPage } from "@/features/workflows/WorkflowDetailPage";

export const Route = createLazyFileRoute("/_app/agents/$agentId/workflows/$workflowId")({
  component: WorkflowDetailPage,
});
