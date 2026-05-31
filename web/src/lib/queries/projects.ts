import { queryOptions } from "@tanstack/react-query";
import { listProjects } from "@/lib/api-client/sdk.gen";

export function agentProjectsOptions(agentId: string) {
  return queryOptions({
    queryKey: ["projects", agentId],
    queryFn: async () => {
      const { data } = await listProjects({ path: { agentID: agentId }, throwOnError: true });
      return data?.projects ?? [];
    },
    enabled: !!agentId,
  });
}
