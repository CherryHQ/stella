import { infiniteQueryOptions, queryOptions } from "@tanstack/react-query";
import { listSessions } from "@/lib/api-client/sdk.gen";
import type { Session } from "@/lib/types";

/** Hand-started, live chats, newest first — the only threads worth listing. */
export function sortedChats(sessions: Session[]): Session[] {
  return sessions
    .filter((session) => session.kind === "chat" && !session.archived)
    .sort((a, b) => new Date(b.last_active).getTime() - new Date(a.last_active).getTime());
}

/**
 * A thread has exactly one home. A chat created inside a project lives in that
 * project and nowhere else, so every agent-level list (sidebar recents, global
 * search) filters through this instead of `sortedChats` — otherwise the same
 * thread shows up twice under two different routes.
 */
export function agentLevelChats(sessions: Session[]): Session[] {
  return sortedChats(sessions).filter((session) => !session.project_id);
}

export function mainSessionQueryOptions(agentId: string) {
  return queryOptions({
    queryKey: ["sessions", agentId, "main"],
    queryFn: async () => {
      const { data } = await listSessions({
        path: { agentId },
        query: { page_size: 1, kind: "main" },
        throwOnError: true,
      });
      return ((data?.sessions as Session[]) ?? [])[0] ?? null;
    },
    enabled: !!agentId,
  });
}

export function projectSessionsQueryOptions(agentId: string, projectId: string) {
  return queryOptions({
    queryKey: ["sessions", agentId, "project", projectId],
    queryFn: async () => {
      const all: Session[] = [];
      let pageToken: string | undefined;
      do {
        const { data } = await listSessions({
          path: { agentId },
          query: { page_size: 200, page_token: pageToken, project_id: projectId },
          throwOnError: true,
        });
        all.push(...((data?.sessions as Session[]) ?? []));
        pageToken = data?.next_page_token ?? undefined;
      } while (pageToken);
      return all;
    },
    enabled: !!agentId && !!projectId,
  });
}

// allChatSessionsQueryOptions walks every page of one agent's chat sessions.
// The sessions API has no server-side search, so the global palette filters the
// full set client-side; `enabled` keeps the walk off the critical path until a
// caller (the search dialog) actually opens.
export function allChatSessionsQueryOptions(agentId: string, enabled = true) {
  return queryOptions({
    queryKey: ["sessions", agentId, "chat", "all"],
    queryFn: async () => {
      const all: Session[] = [];
      let pageToken: string | undefined;
      do {
        const { data } = await listSessions({
          path: { agentId },
          query: { page_size: 200, page_token: pageToken, kind: "chat" },
          throwOnError: true,
        });
        all.push(...((data?.sessions as Session[]) ?? []));
        pageToken = data?.next_page_token ?? undefined;
      } while (pageToken);
      return all;
    },
    enabled: enabled && !!agentId,
  });
}

/**
 * Every chat thread of one agent for the threads management page — the one
 * surface where agent-level and project threads appear together. `projectId`
 * narrows the walk server-side; the "agent only" view is a client filter
 * because the API can express "in project X" but not "in no project".
 */
export function agentThreadsInfiniteQueryOptions(agentId: string, projectId?: string) {
  return infiniteQueryOptions({
    queryKey: ["sessions", agentId, "chat", "threads", projectId ?? ""],
    initialPageParam: undefined as string | undefined,
    queryFn: async ({ pageParam }) => {
      const { data } = await listSessions({
        path: { agentId },
        query: {
          page_size: 30,
          page_token: pageParam,
          kind: "chat",
          ...(projectId ? { project_id: projectId } : {}),
        },
        throwOnError: true,
      });
      return {
        sessions: (data?.sessions as Session[]) ?? [],
        nextPageToken: data?.next_page_token ?? undefined,
      };
    },
    getNextPageParam: (lastPage) => lastPage.nextPageToken,
    enabled: !!agentId,
  });
}

export function sessionsInfiniteQueryOptions(agentId: string, kind?: Session["kind"]) {
  return infiniteQueryOptions({
    queryKey: ["sessions", agentId, kind],
    initialPageParam: undefined as string | undefined,
    queryFn: async ({ pageParam }) => {
      const { data } = await listSessions({
        path: { agentId: agentId },
        query: { page_size: 20, page_token: pageParam, kind },
        throwOnError: true,
      });
      return {
        sessions: (data?.sessions as Session[]) ?? [],
        nextPageToken: data?.next_page_token ?? undefined,
      };
    },
    getNextPageParam: (lastPage) => lastPage.nextPageToken,
  });
}
