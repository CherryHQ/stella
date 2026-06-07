import { createLazyFileRoute, useParams } from "@tanstack/react-router";
import { AutomationsPage } from "@/features/automations/AutomationsPage";

export const Route = createLazyFileRoute("/_app/agents/$agentId/automations/goals/$goalId")({
  component: GoalKeyed,
});

function GoalKeyed() {
  const { agentId, goalId } = useParams({
    from: "/_app/agents/$agentId/automations/goals/$goalId",
  });
  return (
    <AutomationsPage key={`${agentId}/goal/${goalId}`} selectedKind="goal" selectedId={goalId} />
  );
}
