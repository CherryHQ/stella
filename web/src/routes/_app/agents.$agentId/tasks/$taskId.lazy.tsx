import { createLazyFileRoute, useParams } from "@tanstack/react-router";
import { AutomationsPage } from "@/features/automations/AutomationsPage";

export const Route = createLazyFileRoute("/_app/agents/$agentId/tasks/$taskId")({
  component: TaskKeyed,
});

function TaskKeyed() {
  const { agentId, taskId } = useParams({
    from: "/_app/agents/$agentId/tasks/$taskId",
  });
  return (
    <AutomationsPage key={`${agentId}/task/${taskId}`} selectedKind="task" selectedId={taskId} />
  );
}
