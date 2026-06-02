import { useEffect } from "react";
import { useNavigate, useParams } from "@tanstack/react-router";
import { useQueryClient } from "@tanstack/react-query";
import { AutomationPanel } from "@/features/sessions/panels/AutomationPanel";
import { useAppShell } from "@/layouts/AppShell";

export function AutomationNewPage() {
  const { agentId } = useParams({ from: "/_app/agents/$agentId/automations/new" });
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { setHeaderTitle, setHeaderActions } = useAppShell();

  useEffect(() => {
    setHeaderTitle(
      <h1 className="truncate text-[15px] font-semibold tracking-[-0.01em]">New schedule</h1>,
    );
    setHeaderActions(null);
    return () => {
      setHeaderTitle(null);
      setHeaderActions(null);
    };
  }, [setHeaderActions, setHeaderTitle]);

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
