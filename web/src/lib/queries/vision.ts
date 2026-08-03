import { queryOptions } from "@tanstack/react-query";
import { getVisionSettings } from "@/lib/api-client/sdk.gen";

export const visionSettingsQueryOptions = queryOptions({
  queryKey: ["vision-settings"],
  queryFn: async () => {
    const { data } = await getVisionSettings({ throwOnError: true });
    return data;
  },
});
