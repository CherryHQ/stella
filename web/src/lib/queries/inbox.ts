import { infiniteQueryOptions, queryOptions } from "@tanstack/react-query";
import { listInbox } from "@/lib/api-client/sdk.gen";
import type { InboxList } from "@/lib/api-client/types.gen";

export function inboxQueryOptions(agentId?: string, pageSize = 20) {
  return queryOptions({
    queryKey: ["inbox", agentId ?? "", pageSize],
    queryFn: async () => {
      const { data } = await listInbox({
        query: { agent_id: agentId || undefined, page_size: pageSize },
        throwOnError: true,
      });
      return data as InboxList;
    },
  });
}

export function inboxInfiniteQueryOptions(agentId?: string, pageSize = 50) {
  return infiniteQueryOptions({
    queryKey: ["inbox", agentId ?? "", "infinite"],
    initialPageParam: undefined as string | undefined,
    queryFn: async ({ pageParam }) => {
      const { data } = await listInbox({
        query: { agent_id: agentId || undefined, page_size: pageSize, page_token: pageParam },
        throwOnError: true,
      });
      return data as InboxList;
    },
    getNextPageParam: (lastPage) => lastPage.next_page_token ?? undefined,
  });
}
