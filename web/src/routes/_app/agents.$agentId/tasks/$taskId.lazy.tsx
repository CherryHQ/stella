import { createLazyFileRoute, useParams } from "@tanstack/react-router";
import { TaskPage } from "@/features/tasks/TaskPage";

export const Route = createLazyFileRoute("/_app/agents/$agentId/tasks/$taskId")({
  component: TaskKeyed,
});

function TaskKeyed() {
  const { agentId, taskId } = useParams({
    from: "/_app/agents/$agentId/tasks/$taskId",
  });
  return <TaskPage key={`${agentId}/task/${taskId}`} />;
}
