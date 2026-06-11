import { queryOptions } from "@tanstack/react-query";
import { getTask, getTaskBlocker, listSchedulerJobRuns } from "@/lib/api-client";
import type { Blocker, ComponentsDep, ComponentsTask } from "@/lib/api-client/types.gen";
import { fetchAllTasks, fetchAllTaskDeps, fetchAllSchedulerJobRuns } from "@/lib/paginated";

export function standaloneTasksOptions(agentId: string, projectId?: string) {
  return queryOptions({
    queryKey: ["standalone-tasks", agentId, projectId],
    queryFn: async (): Promise<ComponentsTask[]> => {
      const all = await fetchAllTasks(agentId, projectId);
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

export function taskBlockerOptions(taskId: string, blockerId: string | undefined) {
  return queryOptions({
    queryKey: ["task-blocker", taskId, blockerId],
    queryFn: async (): Promise<Blocker> => {
      const { data } = await getTaskBlocker({
        path: { taskId, blockerId: blockerId! },
        throwOnError: true,
      });
      return data as Blocker;
    },
    enabled: !!taskId && !!blockerId,
  });
}

export function taskDepsOptions(taskId: string, enabled = true) {
  return queryOptions({
    queryKey: ["task-deps", taskId],
    queryFn: (): Promise<ComponentsDep[]> => fetchAllTaskDeps(taskId),
    enabled: !!taskId && enabled,
  });
}
