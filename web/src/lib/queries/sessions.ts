import { infiniteQueryOptions } from "@tanstack/react-query";
import { api } from "@/lib/api";
import type { Session } from "@/lib/types";

export function sessionsInfiniteQueryOptions(agentId: string, kind?: string) {
  return infiniteQueryOptions({
    queryKey: ["sessions", agentId, kind],
    initialPageParam: 0,
    queryFn: ({ pageParam }) => {
      const agentParam = agentId ? `&agent_id=${encodeURIComponent(agentId)}` : "";
      const kindParam = kind ? `&kind=${encodeURIComponent(kind)}` : "";
      return api<Session[]>(
        "GET",
        `/api/sessions?limit=20&offset=${pageParam}${agentParam}${kindParam}`,
      );
    },
    getNextPageParam: (lastPage, allPages) =>
      lastPage.length === 20 ? allPages.reduce((sum, p) => sum + p.length, 0) : undefined,
  });
}
