import { queryOptions } from "@tanstack/react-query";
import { listProjects } from "@/lib/api-client/sdk.gen";
import { unwrapApiList } from "@/lib/api-data";
import type { Project } from "@/lib/types";

export function agentProjectsOptions(agentId: string) {
  return queryOptions({
    queryKey: ["projects", agentId],
    queryFn: async () => {
      const { data } = await listProjects({ path: { agentID: agentId }, throwOnError: true });
      return unwrapApiList<Project>(data);
    },
    enabled: !!agentId,
  });
}
