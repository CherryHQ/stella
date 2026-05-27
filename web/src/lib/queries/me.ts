import { queryOptions } from "@tanstack/react-query";
import { getMe } from "@/lib/api-client/sdk.gen";

export const meQueryOptions = queryOptions({
  queryKey: ["me"],
  queryFn: async () => {
    const { data } = await getMe({ throwOnError: true });
    return data;
  },
  retry: false,
});
