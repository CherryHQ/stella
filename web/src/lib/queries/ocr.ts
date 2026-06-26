import { queryOptions } from "@tanstack/react-query";
import { getOcrSettings } from "@/lib/api-client/sdk.gen";

export const ocrSettingsQueryOptions = queryOptions({
  queryKey: ["ocr-settings"],
  queryFn: async () => {
    const { data } = await getOcrSettings({ throwOnError: true });
    return data;
  },
});
