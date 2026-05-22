import { queryOptions } from "@tanstack/react-query";
import { getMe } from "@/lib/api-client/sdk.gen";
import { unwrapApiData } from "@/lib/api-data";
import type { ComponentsMeResponse } from "@/lib/api-client/types.gen";

export const meQueryOptions = queryOptions({
  queryKey: ["me"],
  queryFn: async () => {
    const { data } = await getMe({ throwOnError: true });
    return unwrapApiData<ComponentsMeResponse>(data);
  },
  retry: false,
});
