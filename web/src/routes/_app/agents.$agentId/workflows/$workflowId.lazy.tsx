import { createLazyFileRoute, useParams } from "@tanstack/react-router";
import { WorkflowDetailPage } from "@/features/workflows/WorkflowDetailPage";

export const Route = createLazyFileRoute("/_app/agents/$agentId/workflows/$workflowId")({
  component: WorkflowKeyed,
});

function WorkflowKeyed() {
  const { agentId, workflowId } = useParams({
    from: "/_app/agents/$agentId/workflows/$workflowId",
  });
  return <WorkflowDetailPage key={`${agentId}/workflow/${workflowId}`} />;
}
