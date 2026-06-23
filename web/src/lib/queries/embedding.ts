import { queryOptions } from "@tanstack/react-query";
import { getEmbeddingSettings } from "@/lib/api-client/sdk.gen";

export const embeddingSettingsQueryOptions = queryOptions({
  queryKey: ["embedding-settings"],
  queryFn: async () => {
    const { data } = await getEmbeddingSettings({ throwOnError: true });
    return data;
  },
});
