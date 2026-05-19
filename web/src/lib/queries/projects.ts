import { queryOptions } from "@tanstack/react-query";
import { api } from "@/lib/api";
import type { Project } from "@/lib/types";

export function agentProjectsOptions(agentId: string) {
  return queryOptions({
    queryKey: ["projects", agentId],
    queryFn: () => api<Project[]>("GET", `/api/agents/${agentId}/projects`),
    enabled: !!agentId,
  });
}
