import { queryOptions } from "@tanstack/react-query";
import { getWorkflow, listWorkflowRuns, listWorkflows } from "@/lib/api-client";

export function workflowsOptions(agentId: string) {
  return queryOptions({
    queryKey: ["workflows", agentId],
    queryFn: async () => {
      const { data } = await listWorkflows({
        query: { agent_id: agentId },
        throwOnError: true,
      });
      return data?.workflows ?? [];
    },
    enabled: !!agentId,
  });
}

export function workflowOptions(workflowId: string | undefined) {
  return queryOptions({
    queryKey: ["workflow", workflowId],
    queryFn: async () => {
      const { data } = await getWorkflow({
        path: { id: workflowId! },
        throwOnError: true,
      });
      return data ?? null;
    },
    enabled: !!workflowId,
  });
}

export function workflowRunsOptions(workflowId: string | undefined, limit = 5) {
  return queryOptions({
    queryKey: ["workflow-runs", workflowId, limit],
    queryFn: async () => {
      const { data } = await listWorkflowRuns({
        path: { id: workflowId! },
        query: { page_size: limit },
        throwOnError: true,
      });
      return data ?? { runs: [], total: 0 };
    },
    enabled: !!workflowId,
  });
}
