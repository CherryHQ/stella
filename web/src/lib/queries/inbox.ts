import { queryOptions } from "@tanstack/react-query";
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
