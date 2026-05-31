import { infiniteQueryOptions } from "@tanstack/react-query";
import { listSessions } from "@/lib/api-client/sdk.gen";
import type { Session } from "@/lib/types";

export function sessionsInfiniteQueryOptions(agentId: string, kind?: Session["kind"]) {
  return infiniteQueryOptions({
    queryKey: ["sessions", agentId, kind],
    initialPageParam: 0,
    queryFn: async ({ pageParam }) => {
      const { data } = await listSessions({
        path: { agentID: agentId },
        query: { limit: 20, offset: pageParam as number, kind },
        throwOnError: true,
      });
      return (data?.sessions as Session[]) ?? [];
    },
    getNextPageParam: (lastPage, allPages) =>
      lastPage.length === 20 ? allPages.reduce((sum, p) => sum + p.length, 0) : undefined,
  });
}
