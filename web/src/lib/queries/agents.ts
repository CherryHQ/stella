import { queryOptions } from "@tanstack/react-query";
import { api } from "@/lib/api";
import type { Agent, SchedulerJobList, Skill, UserMemory } from "@/lib/types";

export const agentsQueryOptions = queryOptions({
  queryKey: ["agents"],
  queryFn: () => api<Agent[]>("GET", "/api/agents"),
});

export function agentSchedulerJobsOptions(agentId: string) {
  return queryOptions({
    queryKey: ["agent-scheduler-jobs", agentId],
    queryFn: async () => {
      const res = await api<SchedulerJobList>("GET", "/api/scheduler/jobs");
      return (res.items ?? []).filter((j) => j.owner_kind === "system" || j.agent_id === agentId);
    },
    enabled: !!agentId,
  });
}

export function agentSkillsOptions(agentId: string) {
  return queryOptions({
    queryKey: ["agent-skills", agentId],
    queryFn: async () => {
      const combined =
        (await api<Skill[]>("GET", `/api/agents/${encodeURIComponent(agentId)}/skills`).catch(
          () => [],
        )) ?? [];
      const scopeOrder: Record<string, number> = { system: 0, agent: 1, user: 2 };
      combined.sort((a, b) => {
        const diff = (scopeOrder[a.scope] ?? 9) - (scopeOrder[b.scope] ?? 9);
        return diff !== 0 ? diff : a.name.localeCompare(b.name);
      });
      return combined;
    },
    enabled: !!agentId,
  });
}

export function agentMemoriesOptions(agentId: string) {
  return queryOptions({
    queryKey: ["agent-memories", agentId],
    queryFn: async () => {
      const all = await api<UserMemory[]>("GET", "/api/auth/profile/memories");
      return (all ?? []).filter((m) => m.agent_id === agentId);
    },
    enabled: !!agentId,
  });
}
