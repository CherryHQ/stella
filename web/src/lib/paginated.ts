import { getSessionMessages, listGoals, listSchedulerJobRuns } from "@/lib/api-client";
import type { ComponentsGoal, JobRun } from "@/lib/api-client/types.gen";
import type { Message } from "@/lib/types";
import { sessionMessagesToMessages } from "@/lib/chat-transport";

// offsetPageToken mirrors the server's AIP-158 offset token (encodeOffsetToken
// in internal/server/response.go: base64url of the decimal row offset). It lets
// a numbered pager jump straight to any page without walking cursor tokens.
// Offset 0 returns "" so the caller omits page_token and starts at the first page.
export function offsetPageToken(offset: number): string {
  if (offset <= 0) return "";
  return btoa(String(offset)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

// fetchAllGoals walks every page of root goals for one agent —
// for aggregate views (the overview hub) that need the whole set rather than a
// single server page. The list page uses goalsPageOptions instead.
export async function fetchAllGoals(
  agentId?: string,
  archived?: boolean,
): Promise<ComponentsGoal[]> {
  const all: ComponentsGoal[] = [];
  let pageToken: string | undefined;
  do {
    const { data } = await listGoals({
      query: { agent_id: agentId, archived, page_size: 500, page_token: pageToken },
      throwOnError: true,
    });
    all.push(...(data?.goals ?? []));
    pageToken = data?.next_page_token ?? undefined;
  } while (pageToken);
  return all;
}

// fetchAllSubtree walks the whole root_id family (every node in one tree) for
// graph/tree views that render the full decomposition at once.
export async function fetchAllSubtree(rootId: string): Promise<ComponentsGoal[]> {
  const all: ComponentsGoal[] = [];
  let pageToken: string | undefined;
  do {
    const { data } = await listGoals({
      query: { root: rootId, page_size: 500, page_token: pageToken },
      throwOnError: true,
    });
    all.push(...(data?.goals ?? []));
    pageToken = data?.next_page_token ?? undefined;
  } while (pageToken);
  return all;
}

export async function fetchAllSessionMessages(
  agentId: string,
  sessionId: string,
  opts: { before?: string; onProgress?: (count: number) => void } = {},
): Promise<Message[]> {
  const pages: Message[][] = [];
  const limit = 200;
  let skip = 0;
  let total = 0;
  while (true) {
    const { data } = await getSessionMessages({
      path: { agentId, sessionId },
      query: { limit, skip, ...(opts.before ? { before: opts.before } : {}) },
      throwOnError: true,
    });
    const batch = sessionMessagesToMessages(data?.messages);
    if (batch.length === 0) break;
    pages.push(batch);
    total += batch.length;
    opts.onProgress?.(total);
    if (batch.length < limit) break;
    skip += batch.length;
  }
  return pages.reverse().flat();
}

export async function fetchAllSchedulerJobRuns(agentId: string, jobId: string): Promise<JobRun[]> {
  const all: JobRun[] = [];
  let pageToken: string | undefined;
  do {
    const { data } = await listSchedulerJobRuns({
      path: { agentId, jobId },
      query: { page_size: 500, page_token: pageToken },
      throwOnError: true,
    });
    all.push(...(data?.runs ?? []));
    pageToken = data?.next_page_token ?? undefined;
  } while (pageToken);
  return all;
}
