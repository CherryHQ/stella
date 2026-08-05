import { queryOptions } from "@tanstack/react-query";
import {
  getClawhubSkill,
  listAgents,
  listAgentSkills,
  listAgentTools,
  listClawhubSkills,
  listProfileMemories,
  listSchedulerJobs,
} from "@/lib/api-client/sdk.gen";
import type { Agent, Skill, Tool, UserMemory } from "@/lib/types";

export function clawhubSkillsOptions(query: string) {
  const q = query.trim();
  return queryOptions({
    queryKey: ["clawhub-skills", q],
    queryFn: async () => {
      const { data } = await listClawhubSkills({
        query: { ...(q ? { q } : {}), page_size: 50 },
        throwOnError: true,
      });
      return data?.skills ?? [];
    },
  });
}

export const agentsQueryOptions = queryOptions({
  queryKey: ["agents"],
  queryFn: async () => {
    const { data } = await listAgents({ throwOnError: true });
    return (data?.agents ?? []) as Agent[];
  },
});

export function clawhubSkillDetailOptions(slug: string) {
  return queryOptions({
    queryKey: ["clawhub-skill", slug],
    queryFn: async () => {
      const { data } = await getClawhubSkill({ path: { slug }, throwOnError: true });
      return data;
    },
    enabled: !!slug,
    staleTime: 5 * 60 * 1000,
  });
}

export function agentSchedulerJobsOptions(agentId: string) {
  return queryOptions({
    queryKey: ["agent-scheduler-jobs", agentId],
    queryFn: async () => {
      const { data } = await listSchedulerJobs({ path: { agentId: agentId }, throwOnError: true });
      return data?.jobs ?? [];
    },
    enabled: !!agentId,
  });
}

export function agentToolsOptions(agentId: string) {
  return queryOptions({
    queryKey: ["agent-tools", agentId],
    queryFn: async () => {
      const { data } = await listAgentTools({ path: { id: agentId }, throwOnError: true });
      return (data?.tools ?? []) as Tool[];
    },
    enabled: !!agentId,
  });
}

export function agentSkillsOptions(agentId: string) {
  return queryOptions({
    queryKey: ["agent-skills", agentId],
    queryFn: async () => {
      // Runtime/session consumers require the complete merged active set.
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
      return ((data?.memories as UserMemory[]) ?? []).filter((m) => m.agent_id === agentId);
    },
    enabled: !!agentId,
  });
}
