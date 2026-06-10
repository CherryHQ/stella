import { queryOptions } from "@tanstack/react-query";
import { getSessionContextItems, getSessionSummary } from "@/lib/api-client/sdk.gen";
import type { SessionContextItem, SessionContextMeta } from "@/lib/api-client/types.gen";

export function sessionContextItemsOptions(agentId: string, sessionId: string) {
  return queryOptions({
    queryKey: ["session-context-items", agentId, sessionId],
    queryFn: async (): Promise<{ items: SessionContextItem[]; meta?: SessionContextMeta }> => {
      // Compaction keeps the active window bounded, so the full ordinal
      // sequence is small; drain all pages instead of truncating the newest
      // items. The loop cap is a runaway guard.
      const items: SessionContextItem[] = [];
      let meta: SessionContextMeta | undefined;
      let pageToken: string | undefined;
      for (let page = 0; page < 20; page++) {
        const { data } = await getSessionContextItems({
          path: { agentId, sessionId },
          query: { page_size: 200, page_token: pageToken },
          throwOnError: true,
        });
        items.push(...(data.items ?? []));
        meta = data.meta;
        pageToken = data.next_page_token ?? undefined;
        if (!pageToken) break;
      }
      return { items, meta };
    },
    enabled: !!agentId && !!sessionId,
  });
}

export function sessionSummaryOptions(agentId: string, sessionId: string, summaryId: string) {
  return queryOptions({
    queryKey: ["session-summary", agentId, sessionId, summaryId],
    queryFn: async () => {
      const { data } = await getSessionSummary({
        path: { agentId, sessionId, summaryId },
        throwOnError: true,
      });
      return data;
    },
    enabled: !!agentId && !!sessionId && !!summaryId,
  });
}
