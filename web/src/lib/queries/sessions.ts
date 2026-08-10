import { infiniteQueryOptions, queryOptions } from "@tanstack/react-query";
import { listSessions } from "@/lib/api-client/sdk.gen";
import type { Session } from "@/lib/types";

/** User-visible conversation threads, newest first. */
export function sortedThreads(sessions: Session[]): Session[] {
  return sessions
    .filter(
      (session) => (session.kind === "chat" || session.kind === "delegate") && !session.archived,
    )
    .sort((a, b) => new Date(b.last_active).getTime() - new Date(a.last_active).getTime());
}

/**
 * A thread has exactly one home. A chat created inside a project lives in that
 * project and nowhere else, so every agent-level list (sidebar recents, global
 * search) filters through this instead of `sortedThreads` — otherwise the same
 * thread shows up twice under two different routes.
 */
export function agentLevelThreads(sessions: Session[]): Session[] {
  return sortedThreads(sessions).filter((session) => !session.project_id);
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
    refetchInterval: 3000,
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
    refetchInterval: 3000,
  });
}

// allThreadSessionsQueryOptions walks every page of one agent's sessions, then
// leaves the visible-kind filter to sortedThreads. Session-created delegate
// threads must remain discoverable alongside hand-started chat threads.
// The sessions API has no server-side search, so the global palette filters the
// full set client-side; `enabled` keeps the walk off the critical path until a
// caller (the search dialog) actually opens.
export function allThreadSessionsQueryOptions(agentId: string, enabled = true) {
  return queryOptions({
    queryKey: ["sessions", agentId, "thread", "all"],
    queryFn: async () => {
      const all: Session[] = [];
      let pageToken: string | undefined;
      do {
        const { data } = await listSessions({
          path: { agentId },
          query: { page_size: 200, page_token: pageToken },
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
    queryFn: ({ pageParam }) => fetchVisibleThreadPage(agentId, pageParam, 30, projectId),
    getNextPageParam: (lastPage) => lastPage.nextPageToken,
    enabled: !!agentId,
  });
}

export function threadSessionsInfiniteQueryOptions(agentId: string) {
  return infiniteQueryOptions({
    queryKey: ["sessions", agentId, "thread"],
    initialPageParam: undefined as string | undefined,
    queryFn: ({ pageParam }) => fetchVisibleThreadPage(agentId, pageParam, 20),
    getNextPageParam: (lastPage) => lastPage.nextPageToken,
    enabled: !!agentId,
    refetchInterval: 3000,
  });
}

// A raw page may contain only main/task/scheduler rows. Keep walking until the
// UI has something it can render or the server is exhausted; otherwise an
// empty first page hides the only control that could fetch a later delegate.
async function fetchVisibleThreadPage(
  agentId: string,
  initialPageToken: string | undefined,
  pageSize: number,
  projectId?: string,
) {
  const sessions: Session[] = [];
  let pageToken = initialPageToken;
  do {
    const { data } = await listSessions({
      path: { agentId },
      query: {
        page_size: pageSize,
        page_token: pageToken,
        ...(projectId ? { project_id: projectId } : {}),
      },
      throwOnError: true,
    });
    sessions.push(...sortedThreads((data?.sessions as Session[]) ?? []));
    pageToken = data?.next_page_token ?? undefined;
  } while (sessions.length < pageSize && pageToken);

  return { sessions, nextPageToken: pageToken };
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
    refetchInterval: 3000,
  });
}
