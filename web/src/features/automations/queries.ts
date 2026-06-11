import { queryOptions } from "@tanstack/react-query";
import { getTask, listSchedulerJobRuns } from "@/lib/api-client";
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

/** First page only — enough for the overview sparkline without paging. */
export function schedulerJobRecentRunsOptions(agentId: string, jobId: string) {
  return queryOptions({
    queryKey: ["scheduler-job-recent-runs", agentId, jobId],
    queryFn: async () => {
      const { data } = await listSchedulerJobRuns({
        path: { agentId, jobId },
        query: { page_size: 10 },
        throwOnError: true,
      });
      return data?.runs ?? [];
    },
    enabled: !!agentId && !!jobId,
  });
}

export function taskOptions(taskId: string) {
  return queryOptions({
    queryKey: ["task", taskId],
    queryFn: async (): Promise<ComponentsTask> => {
      const { data } = await getTask({ path: { taskId }, throwOnError: true });
      return data as ComponentsTask;
    },
    enabled: !!taskId,
  });
}
