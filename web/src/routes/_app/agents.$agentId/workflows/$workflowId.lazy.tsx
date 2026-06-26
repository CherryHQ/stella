import { createLazyFileRoute, useParams } from "@tanstack/react-router";
import { WorkflowPage } from "@/features/workflows/WorkflowPage";

function Keyed() {
  const { agentId, workflowId } = useParams({
    from: "/_app/agents/$agentId/workflows/$workflowId",
  });
  return <WorkflowPage key={`${agentId}/workflow/${workflowId}`} />;
}

export const Route = createLazyFileRoute("/_app/agents/$agentId/workflows/$workflowId")({
  component: Keyed,
});
