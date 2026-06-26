import { keepPreviousData, queryOptions } from "@tanstack/react-query";
import { getWorkflow, listWorkflows } from "@/lib/api-client";
import type { Workflow } from "@/lib/api-client/types.gen";
import { offsetPageToken } from "@/lib/paginated";

export const WORKFLOWS_PAGE_SIZE = 24;

export interface WorkflowsPageParams {
  agentId: string;
  /** case-insensitive substring on name/intent; server-side. */
  q?: string;
  /** 1-based page. */
  page?: number;
}

export interface WorkflowsPage {
  workflows: Workflow[];
  total: number;
}

// workflowsPageOptions drives the list page from one server page so the first
// paint never waits for every workflow to download.
export function workflowsPageOptions(p: WorkflowsPageParams) {
  const page = Math.max(1, p.page ?? 1);
  return queryOptions({
    queryKey: ["workflows-page", p.agentId, p.q ?? "", page],
    queryFn: async (): Promise<WorkflowsPage> => {
      const { data } = await listWorkflows({
        query: {
          agent_id: p.agentId,
          q: p.q || undefined,
          page_size: WORKFLOWS_PAGE_SIZE,
          page_token: offsetPageToken((page - 1) * WORKFLOWS_PAGE_SIZE),
        },
        throwOnError: true,
      });
      return { workflows: data?.workflows ?? [], total: data?.total ?? 0 };
    },
    enabled: !!p.agentId,
    // Keep the previous page on screen while the next loads, so the pager's
    // total never transiently drops to 0.
    placeholderData: keepPreviousData,
  });
}

export function workflowOptions(id: string | undefined) {
  return queryOptions({
    queryKey: ["workflow", id],
    queryFn: async () => {
      const { data } = await getWorkflow({ path: { id: id! }, throwOnError: true });
      return data as Workflow;
    },
    enabled: !!id,
  });
}
