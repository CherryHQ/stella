import { queryOptions } from "@tanstack/react-query";
import {
  listAgents,
  listAgentSkills,
  listProfileMemories,
  listSchedulerJobs,
} from "@/lib/api-client/sdk.gen";
import type { Agent, Skill, UserMemory } from "@/lib/types";

export const agentsQueryOptions = queryOptions({
  queryKey: ["agents"],
  queryFn: async () => {
    const { data } = await listAgents({ throwOnError: true });
    return (data?.agents ?? []) as Agent[];
  },
});

export function agentSchedulerJobsOptions(agentId: string) {
  return queryOptions({
    queryKey: ["agent-scheduler-jobs", agentId],
    queryFn: async () => {
      const { data } = await listSchedulerJobs({ path: { agentID: agentId }, throwOnError: true });
      return data?.jobs ?? [];
    },
    enabled: !!agentId,
  });
}

export function agentSkillsOptions(agentId: string) {
  return queryOptions({
    queryKey: ["agent-skills", agentId],
    queryFn: async () => {
      const combined =
        (await listAgentSkills({ path: { id: agentId }, throwOnError: true })
          .then(({ data }) => data?.skills ?? [])
          .catch(() => [])) ?? [];
      const scopeOrder: Record<string, number> = { system: 0, agent: 1, user: 2 };
      combined.sort((a, b) => {
        const diff = (scopeOrder[a.scope ?? ""] ?? 9) - (scopeOrder[b.scope ?? ""] ?? 9);
        return diff !== 0 ? diff : (a.name ?? "").localeCompare(b.name ?? "");
      });
      return combined as Skill[];
    },
    enabled: !!agentId,
  });
}

export function agentMemoriesOptions(agentId: string) {
  return queryOptions({
    queryKey: ["agent-memories", agentId],
    queryFn: async () => {
      const { data } = await listProfileMemories({ throwOnError: true });
      return ((data as UserMemory[]) ?? []).filter((m) => m.agent_id === agentId);
    },
    enabled: !!agentId,
  });
}
