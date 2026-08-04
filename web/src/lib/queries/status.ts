import { queryOptions } from "@tanstack/react-query";
import { getStatus } from "@/lib/api-client/sdk.gen";

// Polled so a long-lived tab notices the server being upgraded under it.
// /api/status is public and tiny, so a slow poll costs nothing; staleTime 0 is
// what lets the window-focus refetch actually fire, which is when the check
// matters most — someone coming back to a tab they left open overnight.
export const statusQueryOptions = queryOptions({
  queryKey: ["status"],
  queryFn: async () => {
    const { data } = await getStatus({ throwOnError: true });
    return data;
  },
  refetchInterval: 5 * 60_000,
  staleTime: 0,
  retry: false,
});
