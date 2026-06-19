import { createLazyFileRoute, useParams } from "@tanstack/react-router";
import { DeliverablePage } from "@/features/deliverables/DeliverablePage";

export const Route = createLazyFileRoute("/_app/agents/$agentId/deliverables/$deliverableId")({
  component: DeliverableKeyed,
});

function DeliverableKeyed() {
  const { agentId, deliverableId } = useParams({
    from: "/_app/agents/$agentId/deliverables/$deliverableId",
  });
  return <DeliverablePage key={`${agentId}/deliverable/${deliverableId}`} />;
}
