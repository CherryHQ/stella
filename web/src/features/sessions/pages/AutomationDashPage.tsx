import { useNavigate, useParams } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { agentSchedulerJobsOptions } from "@/lib/queries/agents";
import { AutomationDashPanel } from "@/features/sessions/panels/AutomationDashPanel";

export function AutomationDashPage() {
  const { agentId } = useParams({ from: "/_app/agents/$agentId/automations/" });
  const navigate = useNavigate();
  const { data: schedulerJobs = [] } = useQuery(agentSchedulerJobsOptions(agentId));

  return (
    <AutomationDashPanel
      agentId={agentId}
      schedulerJobs={schedulerJobs}
      onCreateJob={() => {
        void navigate({ to: "/agents/$agentId/automations/new", params: { agentId } });
      }}
      onSelectJob={(jobId) => {
        void navigate({
          to: "/agents/$agentId/automations/$jobId",
          params: { agentId, jobId },
        });
      }}
    />
  );
}
