import { queryOptions } from "@tanstack/react-query";
import { listScopedSkills } from "@/lib/api-client/sdk.gen";
import type { Skill } from "@/lib/types";

export type ScopedSkillScope = "user" | "user_agent" | "system" | "system_agent";

// Query key must include scope + agent_id so each scope/agent slice caches
// independently (agent scopes are keyed per-agent).
export function scopedSkillsQueryOptions(scope: ScopedSkillScope, agentID?: string) {
  return queryOptions({
    queryKey: ["scoped-skills", scope, agentID ?? null],
    queryFn: async () => {
      const { data } = await listScopedSkills({
        query: { scope, agent_id: agentID },
        throwOnError: true,
      });
      // SAFETY: listSkills returns skill items under data.skills.
      return (data?.skills as Skill[]) ?? [];
    },
  });
}
