import { useCallback, useMemo } from "react";
import { infiniteQueryOptions, queryOptions, useQuery } from "@tanstack/react-query";
import { listInbox } from "@/lib/api-client/sdk.gen";
import type { InboxList } from "@/lib/api-client/types.gen";
import { agentsQueryOptions } from "@/lib/queries/agents";

// An inbox item names its source but never spells it, and the two surfaces that
// render one — the Inbox page and the sidebar bell's preview of it — have to say
// the same words. One map, so the preview can never drift from the page.
export const INBOX_SOURCE_LABELS = {
  goal: "inbox.source.goal",
  scheduler_run: "inbox.source.scheduler_run",
} as const;

/**
 * Names the agent behind an inbox item. Items carry an agent id only; the agent
 * list is already cached for the sidebar, so naming the owner costs no API field
 * and no request — and without it a cross-agent list cannot say which agent is
 * waiting. An id with no match yields "", so callers treat "no agent" and "not
 * loaded yet" alike.
 */
export function useInboxAgentName(): (agentId?: string | null) => string {
  const { data: agents = [] } = useQuery(agentsQueryOptions);
  const names = useMemo(() => new Map(agents.map((a) => [a.id, a.name])), [agents]);
  return useCallback((agentId?: string | null) => (agentId && names.get(agentId)) || "", [names]);
}

export function inboxQueryOptions(agentId?: string, pageSize = 20) {
  return queryOptions({
    queryKey: ["inbox", agentId ?? "", pageSize],
    queryFn: async () => {
      const { data } = await listInbox({
        query: { agent_id: agentId || undefined, page_size: pageSize },
        throwOnError: true,
      });
      // SAFETY: listInbox returns an InboxList on success.
      return data as InboxList;
    },
  });
}

export function inboxInfiniteQueryOptions(agentId?: string, pageSize = 50) {
  return infiniteQueryOptions({
    queryKey: ["inbox", agentId ?? "", "infinite"],
    // SAFETY: inbox infinite query page param is pinned to the string token.
    initialPageParam: undefined as string | undefined,
    queryFn: async ({ pageParam }) => {
      const { data } = await listInbox({
        query: { agent_id: agentId || undefined, page_size: pageSize, page_token: pageParam },
        throwOnError: true,
      });
      // SAFETY: listInbox returns an InboxList on success.
      return data as InboxList;
    },
    getNextPageParam: (lastPage) => lastPage.next_page_token ?? undefined,
  });
}
