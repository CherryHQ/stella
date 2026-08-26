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
      // SAFETY: this query asks for a single main session, so item 0 is that main.
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
        // SAFETY: listSessions returns session items under data.sessions.
        all.push(...((data?.sessions as Session[]) ?? []));
        pageToken = data?.next_page_token ?? undefined;
      } while (pageToken);
      return all;
    },
    enabled: !!agentId && !!projectId,
    refetchInterval: 3000,
  });
}

// The API deliberately excludes internal kinds when `kind` is omitted, so a
// visible thread list must request chat and delegate explicitly. Fetch both
// complete ordered streams and merge them client-side; revisit this when the
// API can express a multi-kind filter or per-agent thread counts become large.
// The sessions API has no server-side search, so the global palette filters the
// full set client-side; `enabled` keeps the walk off the critical path until a
// caller (the search dialog) actually opens.
export function allThreadSessionsQueryOptions(agentId: string, enabled = true, projectId?: string) {
  return queryOptions({
    queryKey: ["sessions", agentId, "thread", "all", projectId ?? ""],
    queryFn: async () => {
      const [chats, delegates] = await Promise.all([
        listAllSessionsByKind(agentId, "chat", projectId),
        listAllSessionsByKind(agentId, "delegate", projectId),
      ]);
      return sortedThreads([...chats, ...delegates]);
    },
    enabled: enabled && !!agentId,
  });
}

async function listAllSessionsByKind(
  agentId: string,
  kind: Extract<Session["kind"], "chat" | "delegate">,
  projectId?: string,
): Promise<Session[]> {
  const all: Session[] = [];
  let pageToken: string | undefined;
  do {
    const { data } = await listSessions({
      path: { agentId },
      query: {
        page_size: 200,
        page_token: pageToken,
        kind,
        ...(projectId ? { project_id: projectId } : undefined),
      },
      throwOnError: true,
    });
    // SAFETY: listSessions returns session items under data.sessions.
    all.push(...((data?.sessions as Session[]) ?? []));
    pageToken = data?.next_page_token ?? undefined;
  } while (pageToken);
  return all;
}

/** Generic server pagination for callers that need one kind or the API's
 * default external-session view. Visible conversation lists must use
 * allThreadSessionsQueryOptions so delegate rows are requested explicitly. */
export function sessionsInfiniteQueryOptions(agentId: string, kind?: Session["kind"]) {
  return infiniteQueryOptions({
    queryKey: ["sessions", agentId, kind],
    // SAFETY: sessions infinite query param is pinned to the string token.
    initialPageParam: undefined as string | undefined,
    queryFn: async ({ pageParam }) => {
      const { data } = await listSessions({
        path: { agentId: agentId },
        query: { page_size: 20, page_token: pageParam, kind },
        throwOnError: true,
      });
      return {
        // SAFETY: listSessions returns session items under data.sessions.
        sessions: (data?.sessions as Session[]) ?? [],
        nextPageToken: data?.next_page_token ?? undefined,
      };
    },
    getNextPageParam: (lastPage) => lastPage.nextPageToken,
    refetchInterval: 3000,
  });
}
