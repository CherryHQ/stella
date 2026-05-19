import { infiniteQueryOptions } from "@tanstack/react-query";
import { api } from "@/lib/api";
import type { Session } from "@/lib/types";

export function sessionsInfiniteQueryOptions(agentId: string, source?: string) {
  return infiniteQueryOptions({
    queryKey: ["sessions", agentId, source],
    initialPageParam: 0,
    queryFn: ({ pageParam }) => {
      const agentParam = agentId ? `&agent_id=${encodeURIComponent(agentId)}` : "";
      const sourceParam = source ? `&source=${encodeURIComponent(source)}` : "";
      return api<Session[]>(
        "GET",
        `/api/sessions?limit=20&offset=${pageParam}${agentParam}${sourceParam}`,
      );
    },
    getNextPageParam: (lastPage, allPages) =>
      lastPage.length === 20 ? allPages.reduce((sum, p) => sum + p.length, 0) : undefined,
  });
}
