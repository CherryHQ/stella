import { queryOptions } from "@tanstack/react-query";
import { listProfileChangelog, listProfileConstraints } from "@/lib/api-client";

export function constraintsQueryOptions(agentId: string) {
  return queryOptions({
    queryKey: ["agent-constraints", agentId],
    queryFn: async () => {
      const { data } = await listProfileConstraints({
        path: { agentId },
        throwOnError: true,
      });
      return data.constraints;
    },
    enabled: !!agentId,
  });
}

export function changelogQueryOptions(agentId: string, scope?: string) {
  return queryOptions({
    queryKey: ["agent-changelog", agentId, scope],
    queryFn: async () => {
      const { data } = await listProfileChangelog({
        path: { agentId },
        query: { scope, limit: 20 },
        throwOnError: true,
      });
      return data.entries;
    },
    enabled: !!agentId,
  });
}
