import { queryOptions } from "@tanstack/react-query";
import { listJobTemplates } from "@/lib/api-client/sdk.gen";
import type { ComponentsJobTemplate } from "@/lib/api-client/types.gen";

export const jobTemplatesQueryOptions = queryOptions({
  queryKey: ["job-templates"],
  queryFn: async (): Promise<ComponentsJobTemplate[]> => {
    const { data } = await listJobTemplates({ throwOnError: true });
    return data?.job_templates ?? [];
  },
});
