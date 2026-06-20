import { queryOptions } from "@tanstack/react-query";
import { listJobTemplates, listSchedulerJobRuns } from "@/lib/api-client/sdk.gen";
import type { ComponentsJobTemplate } from "@/lib/api-client/types.gen";
import { fetchAllSchedulerJobRuns } from "@/lib/paginated";

export const jobTemplatesQueryOptions = queryOptions({
  queryKey: ["job-templates"],
  queryFn: async (): Promise<ComponentsJobTemplate[]> => {
    const { data } = await listJobTemplates({ throwOnError: true });
    return data?.job_templates ?? [];
  },
});

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
