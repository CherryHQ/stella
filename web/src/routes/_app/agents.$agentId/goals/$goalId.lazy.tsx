import { createLazyFileRoute, useParams } from "@tanstack/react-router";
import { GoalPage } from "@/features/goals/GoalPage";

export const Route = createLazyFileRoute("/_app/agents/$agentId/goals/$goalId")({
  component: GoalKeyed,
});

function GoalKeyed() {
  const { agentId, goalId } = useParams({
    from: "/_app/agents/$agentId/goals/$goalId",
  });
  return <GoalPage key={`${agentId}/goal/${goalId}`} />;
}
