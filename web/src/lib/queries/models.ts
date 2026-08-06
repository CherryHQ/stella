import { queryOptions } from "@tanstack/react-query";
import { listModels } from "@/lib/api-client/sdk.gen";

export const modelsQueryOptions = queryOptions({
  queryKey: ["models"],
  queryFn: async () => {
    const { data } = await listModels({ throwOnError: true });
    return data?.models ?? [];
  },
});
