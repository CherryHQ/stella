import { useNavigate, useParams } from "@tanstack/react-router";
import { TaskBoardPanel } from "@/features/sessions/panels/TaskBoardPanel";

export function TaskBoardPage() {
  const { agentId } = useParams({ from: "/_app/agents/$agentId/tasks/" });
  const navigate = useNavigate();

  return (
    <TaskBoardPanel
      agentId={agentId}
      onOpenTaskSession={(sessionId) => {
        void navigate({
          to: "/agents/$agentId/sessions/$sessionId",
          params: { agentId, sessionId },
        });
      }}
    />
  );
}
