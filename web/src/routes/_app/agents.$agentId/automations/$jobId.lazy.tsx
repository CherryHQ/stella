import { createLazyFileRoute, useParams } from "@tanstack/react-router";
import { AutomationDashPage } from "@/features/sessions/pages/AutomationDashPage";

export const Route = createLazyFileRoute("/_app/agents/$agentId/automations/$jobId")({
  component: AutomationDashJobKeyed,
});

function AutomationDashJobKeyed() {
  const { agentId, jobId } = useParams({
    from: "/_app/agents/$agentId/automations/$jobId",
  });
  return <AutomationDashPage key={`${agentId}/${jobId}`} />;
}
