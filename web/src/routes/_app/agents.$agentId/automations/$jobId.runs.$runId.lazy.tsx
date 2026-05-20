import { createLazyFileRoute, useParams } from "@tanstack/react-router";
import { AutomationDashPage } from "@/features/sessions/pages/AutomationDashPage";

export const Route = createLazyFileRoute("/_app/agents/$agentId/automations/$jobId/runs/$runId")({
  component: AutomationDashRunKeyed,
});

function AutomationDashRunKeyed() {
  const { agentId, jobId, runId } = useParams({
    from: "/_app/agents/$agentId/automations/$jobId/runs/$runId",
  });
  return <AutomationDashPage key={`${agentId}/${jobId}/${runId}`} />;
}
