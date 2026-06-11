import { createLazyFileRoute, useParams } from "@tanstack/react-router";
import { GoalPage } from "@/features/automations/GoalPage";

export const Route = createLazyFileRoute("/_app/agents/$agentId/tasks/goals/$goalId")({
  component: GoalKeyed,
});

function GoalKeyed() {
  const { agentId, goalId } = useParams({
    from: "/_app/agents/$agentId/tasks/goals/$goalId",
  });
  return <GoalPage key={`${agentId}/goal/${goalId}`} />;
}
