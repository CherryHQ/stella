import { useNavigate, useParams } from "@tanstack/react-router";
import { useQueryClient } from "@tanstack/react-query";
import { AutomationPanel } from "@/features/sessions/panels/AutomationPanel";

export function AutomationNewPage() {
  const { agentId } = useParams({ from: "/_app/agents/$agentId/automations/new" });
  const navigate = useNavigate();
  const queryClient = useQueryClient();

  return (
    <AutomationPanel
      jobId={null}
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
