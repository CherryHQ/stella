import { queryOptions } from "@tanstack/react-query";
import type { ComponentsTask } from "@/lib/api-client/types.gen";
import { fetchAllTasks, fetchAllSchedulerJobRuns } from "@/lib/paginated";

export function standaloneTasksOptions(agentId: string) {
  return queryOptions({
    queryKey: ["standalone-tasks", agentId],
    queryFn: async (): Promise<ComponentsTask[]> => {
      const all = await fetchAllTasks(agentId);
      return all.filter((t) => !t.goal_id);
    },
    enabled: !!agentId,
  });
}

export function schedulerJobRunsOptions(agentId: string, jobId: string) {
  return queryOptions({
    queryKey: ["scheduler-job-runs", agentId, jobId],
    queryFn: () => fetchAllSchedulerJobRuns(agentId, jobId),
    enabled: !!agentId && !!jobId,
  });
}
