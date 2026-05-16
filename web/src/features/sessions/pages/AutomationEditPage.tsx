import { useNavigate, useParams } from "@tanstack/react-router";
import { useQueryClient } from "@tanstack/react-query";
import { AutomationPanel } from "@/features/sessions/panels/AutomationPanel";

export function AutomationEditPage() {
  const params = useParams({ strict: false }) as { agentId: string; jobId: string };
  const { agentId, jobId } = params;
  const navigate = useNavigate();
  const queryClient = useQueryClient();

  return (
    <AutomationPanel
      jobId={jobId}
      agentId={agentId}
      onSaved={() => {
        void queryClient.invalidateQueries({ queryKey: ["agent-scheduler-jobs", agentId] });
        void navigate({ to: "/agents/$agentId/automations", params: { agentId } });
      }}
      onDeleted={() => {
        void queryClient.invalidateQueries({ queryKey: ["agent-scheduler-jobs", agentId] });
        void navigate({ to: "/agents/$agentId/automations", params: { agentId } });
      }}
    />
  );
}
