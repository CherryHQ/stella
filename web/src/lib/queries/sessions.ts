import { infiniteQueryOptions } from "@tanstack/react-query";
import { api } from "@/lib/api";
import type { Session } from "@/lib/types";

export function sessionsInfiniteQueryOptions(agentId: string, kind?: string) {
  return infiniteQueryOptions({
    queryKey: ["sessions", agentId, kind],
    initialPageParam: 0,
    queryFn: ({ pageParam }) => {
      const kindParam = kind ? `&kind=${encodeURIComponent(kind)}` : "";
      return api<Session[]>(
        "GET",
        `/api/agents/${encodeURIComponent(agentId)}/sessions?limit=20&offset=${pageParam}${kindParam}`,
      );
    },
    getNextPageParam: (lastPage, allPages) =>
      lastPage.length === 20 ? allPages.reduce((sum, p) => sum + p.length, 0) : undefined,
  });
}
