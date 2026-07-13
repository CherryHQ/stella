import { queryOptions } from "@tanstack/react-query";
import { getProfileMemory, listProfileChangelog, listProfileConstraints } from "@/lib/api-client";
import type { ListProfileChangelogData } from "@/lib/api-client";
import type { UserMemory } from "@/lib/types";

type ChangelogScope = NonNullable<ListProfileChangelogData["query"]>["scope"];

export function agentMemoryOptions(agentId: string) {
  return queryOptions({
    queryKey: ["agent-memory", agentId],
    queryFn: async () => {
      const { data } = await getProfileMemory({
        path: { agentId },
        throwOnError: true,
      });
      return data as UserMemory;
    },
    enabled: !!agentId,
  });
}

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

export function changelogQueryOptions(agentId: string, scope?: ChangelogScope) {
  return queryOptions({
    queryKey: ["agent-changelog", agentId, scope],
    queryFn: async () => {
      const { data } = await listProfileChangelog({
        path: { agentId },
        query: { scope, page_size: 20 },
        throwOnError: true,
      });
      return data.entries;
    },
    enabled: !!agentId,
  });
}
