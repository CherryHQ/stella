import { infiniteQueryOptions, queryOptions } from "@tanstack/react-query";
import {
  getProfileMemory,
  listProfileChangelog,
  listProfileConstraints,
  listProfileKnowledge,
} from "@/lib/api-client/sdk.gen";
import type { ListProfileChangelogData } from "@/lib/api-client";
import type { ComponentsChangelogList, ComponentsKnowledgeList } from "@/lib/api-client/types.gen";
import type { UserMemory } from "@/lib/types";

type ChangelogScope = NonNullable<ListProfileChangelogData["query"]>["scope"];
export type KnowledgeState = "active" | "removed";

const MEMORY_PAGE_SIZE = 20;

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

export function knowledgeInfiniteQueryOptions(agentId: string, state: KnowledgeState) {
  return infiniteQueryOptions({
    queryKey: ["agent-knowledge", agentId, state, MEMORY_PAGE_SIZE],
    initialPageParam: undefined as string | undefined,
    queryFn: async ({ pageParam }) => {
      const { data } = await listProfileKnowledge({
        path: { agentId },
        query: {
          state,
          page_size: MEMORY_PAGE_SIZE,
          ...(pageParam ? { page_token: pageParam } : {}),
        },
        throwOnError: true,
      });
      return data;
    },
    getNextPageParam: (lastPage) => lastPage.next_page_token ?? undefined,
    enabled: !!agentId,
  });
}

export function memoryChangelogInfiniteQueryOptions(agentId: string, scope?: ChangelogScope) {
  return infiniteQueryOptions({
    queryKey: ["agent-changelog-pages", agentId, scope ?? "all", MEMORY_PAGE_SIZE],
    initialPageParam: undefined as string | undefined,
    queryFn: async ({ pageParam }) => {
      const { data } = await listProfileChangelog({
        path: { agentId },
        query: {
          ...(scope ? { scope } : {}),
          page_size: MEMORY_PAGE_SIZE,
          ...(pageParam ? { page_token: pageParam } : {}),
        },
        throwOnError: true,
      });
      return data;
    },
    getNextPageParam: (lastPage) => lastPage.next_page_token ?? undefined,
    enabled: !!agentId,
  });
}

export function flattenKnowledgePages(pages?: ComponentsKnowledgeList[]) {
  return pages?.flatMap((page) => page.knowledge) ?? [];
}

export function flattenMemoryChangelogPages(pages?: ComponentsChangelogList[]) {
  return pages?.flatMap((page) => page.entries) ?? [];
}
