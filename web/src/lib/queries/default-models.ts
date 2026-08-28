import { queryOptions } from "@tanstack/react-query";
import { getDefaultModels } from "@/lib/api-client/sdk.gen";

export const defaultModelsQueryOptions = queryOptions({
  queryKey: ["default-models"],
  queryFn: async () => {
    const { data } = await getDefaultModels({ throwOnError: true });
    return data;
  },
});
