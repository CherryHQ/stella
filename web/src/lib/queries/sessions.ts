import { infiniteQueryOptions } from "@tanstack/react-query";
import { listSessions } from "@/lib/api-client/sdk.gen";
import type { Session } from "@/lib/types";

export function sessionsInfiniteQueryOptions(agentId: string, kind?: Session["kind"]) {
  return infiniteQueryOptions({
    queryKey: ["sessions", agentId, kind],
    initialPageParam: undefined as string | undefined,
    queryFn: async ({ pageParam }) => {
      const { data } = await listSessions({
        path: { agentID: agentId },
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
