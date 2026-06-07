import { useEffect } from "react";
import { useNavigate, useParams } from "@tanstack/react-router";
import { useQueryClient } from "@tanstack/react-query";
import { AutomationPanel } from "@/features/sessions/panels/AutomationPanel";
import { useAppShell } from "@/layouts/AppShell";
import { useI18n } from "@/lib/i18n";

export function AutomationEditPage() {
  const params = useParams({ strict: false }) as { agentId: string; jobId: string };
  const { agentId, jobId } = params;
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { setHeaderTitle, setHeaderActions } = useAppShell();
  const { t } = useI18n();

  useEffect(() => {
    setHeaderTitle(
      <h1 className="truncate text-[15px] font-semibold tracking-[-0.01em]">
        {t("sessions.auto.editSchedule")}
      </h1>,
    );
    setHeaderActions(null);
    return () => {
      setHeaderTitle(null);
      setHeaderActions(null);
    };
  }, [setHeaderActions, setHeaderTitle]);

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
