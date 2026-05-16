import { useNavigate, useParams } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { agentSchedulerJobsOptions } from "@/lib/queries/agents";
import { AutomationDashPanel } from "@/features/sessions/panels/AutomationDashPanel";

export function AutomationDashPage() {
  const params = useParams({ strict: false }) as {
    agentId: string;
    jobId?: string;
    runId?: string;
  };
  const { agentId, jobId, runId } = params;
  const navigate = useNavigate();
  const { data: schedulerJobs = [] } = useQuery(agentSchedulerJobsOptions(agentId));

  return (
    <AutomationDashPanel
      agentId={agentId}
      schedulerJobs={schedulerJobs}
      selectedJobId={jobId}
      selectedRunId={runId}
      onSelectJob={(id) => {
        if (id) {
          void navigate({
            to: "/agents/$agentId/automations/$jobId",
            params: { agentId, jobId: id },
          });
        } else {
          void navigate({ to: "/agents/$agentId/automations", params: { agentId } });
        }
      }}
      onSelectRun={(jId, rId) => {
        if (rId) {
          void navigate({
            to: "/agents/$agentId/automations/$jobId/runs/$runId",
            params: { agentId, jobId: jId, runId: rId },
          });
        } else if (jId) {
          void navigate({
            to: "/agents/$agentId/automations/$jobId",
            params: { agentId, jobId: jId },
          });
        } else {
          void navigate({ to: "/agents/$agentId/automations", params: { agentId } });
        }
      }}
      onEditJob={(id) => {
        void navigate({
          to: "/agents/$agentId/automations/$jobId/edit",
          params: { agentId, jobId: id },
        });
      }}
      onCreateJob={() => {
        void navigate({ to: "/agents/$agentId/automations/new", params: { agentId } });
      }}
    />
  );
}
