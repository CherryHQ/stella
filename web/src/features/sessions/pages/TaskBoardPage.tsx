import { useNavigate, useParams } from "@tanstack/react-router";
import { TaskBoardPanel } from "@/features/sessions/panels/TaskBoardPanel";

export function TaskBoardPage() {
  const params = useParams({ strict: false }) as { agentId: string; taskId?: string };
  const { agentId, taskId } = params;
  const navigate = useNavigate();

  return (
    <TaskBoardPanel
      agentId={agentId}
      selectedTaskId={taskId}
      onSelectTask={(id) => {
        if (id) {
          void navigate({
            to: "/agents/$agentId/tasks/$taskId",
            params: { agentId, taskId: id },
          });
        } else {
          void navigate({ to: "/agents/$agentId/tasks", params: { agentId } });
        }
      }}
      onOpenTaskSession={(sessionId) => {
        void navigate({
          to: "/agents/$agentId/sessions/$sessionId",
          params: { agentId, sessionId },
        });
      }}
    />
  );
}
