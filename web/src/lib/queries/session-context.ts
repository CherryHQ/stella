import { queryOptions } from "@tanstack/react-query";
import { getSessionContextItems, getSessionSummary } from "@/lib/api-client/sdk.gen";

export function sessionContextItemsOptions(agentId: string, sessionId: string) {
  return queryOptions({
    queryKey: ["session-context-items", agentId, sessionId],
    queryFn: async () => {
      const { data } = await getSessionContextItems({
        path: { agentId, sessionId },
        query: { page_size: 200 },
        throwOnError: true,
      });
      return data;
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
