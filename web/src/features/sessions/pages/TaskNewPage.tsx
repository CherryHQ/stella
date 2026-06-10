import { useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { useQueryClient } from "@tanstack/react-query";
import type { ComponentsTask } from "@/lib/api-client/types.gen";
import { TaskPanel } from "@/features/sessions/panels/TaskPanel";

export function TaskNewPage() {
  const { agentId } = useParams({ from: "/_app/agents/$agentId/tasks/new" });
  const { project_id: projectId } = useSearch({ from: "/_app/agents/$agentId/tasks/new" });
  const navigate = useNavigate();
  const queryClient = useQueryClient();

  return (
    <TaskPanel
      agentId={agentId}
      projectId={projectId}
      onCreated={(task: ComponentsTask) => {
        void queryClient.invalidateQueries({ queryKey: ["sessions", agentId] });
        if (task.session_id) {
          void navigate({
            to: "/agents/$agentId/sessions/$sessionId",
            params: { agentId, sessionId: task.session_id },
          });
        } else {
          void navigate({ to: "/agents/$agentId/tasks", params: { agentId } });
        }
      }}
    />
  );
}
