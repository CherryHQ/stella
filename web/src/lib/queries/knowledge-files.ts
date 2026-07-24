import { infiniteQueryOptions, queryOptions } from "@tanstack/react-query";
import { listAgents, listKnowledgeFiles } from "@/lib/api-client/sdk.gen";
import type { KnowledgeFileList, KnowledgeFileScope } from "@/lib/api-client/types.gen";
import type { Agent } from "@/lib/types";

export const KNOWLEDGE_FILES_PAGE_SIZE = 50;

export interface KnowledgeFilesQuery {
  scope: KnowledgeFileScope;
  agentId?: string;
  q?: string;
}

// Every owner dimension and search term participates in the key so changing a
// scope never reuses pages fetched for a different knowledge owner.
export function knowledgeFilesQueryKey({ scope, agentId, q }: KnowledgeFilesQuery) {
  return ["knowledge-files", scope, agentId ?? null, q?.trim() ?? ""] as const;
}

export function knowledgeFilesInfiniteQueryOptions({ scope, agentId, q }: KnowledgeFilesQuery) {
  const normalizedQuery = q?.trim() ?? "";
  const needsAgent = scope === "system_agent" || scope === "user_agent";

  return infiniteQueryOptions({
    queryKey: knowledgeFilesQueryKey({ scope, agentId, q: normalizedQuery }),
    enabled: !needsAgent || Boolean(agentId),
    initialPageParam: "",
    queryFn: async ({ pageParam }) => {
      const { data } = await listKnowledgeFiles({
        query: {
          scope,
          agent_id: agentId,
          q: normalizedQuery || undefined,
          page_size: KNOWLEDGE_FILES_PAGE_SIZE,
          page_token: pageParam || undefined,
        },
        throwOnError: true,
      });
      return data as KnowledgeFileList;
    },
    getNextPageParam: (lastPage) => lastPage.next_page_token || undefined,
    // Parsing is asynchronous, but V1 intentionally refreshes only on an
    // explicit browser refresh instead of polling or focus-triggered requests.
    refetchInterval: false,
    refetchOnWindowFocus: false,
  });
}

// Admin agent enumeration uses a distinct cache key because include_all can
// return agents that the current user-facing list intentionally omits.
export const knowledgeAdminAgentsQueryOptions = queryOptions({
  queryKey: ["knowledge-admin-agents"],
  queryFn: async () => {
    const { data } = await listAgents({
      query: { include_all: true },
      throwOnError: true,
    });
    return (data?.agents ?? []) as Agent[];
  },
});
