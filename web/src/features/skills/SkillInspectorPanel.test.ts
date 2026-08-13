import { QueryClient } from "@tanstack/react-query";
import { describe, expect, it } from "vitest";
import type { Skill } from "@/lib/types";
import { refreshSkillMutationBaseline, skillMutationDigest } from "./skill-mutation-baseline";

function skill(contentDigest: string): Skill {
  return { content_digest: contentDigest } as Skill;
}

describe("Skill inspector mutation digest", () => {
  it("uses detail over list data and advances the baseline after a save", async () => {
    const queryClient = new QueryClient();
    const queryKey = ["agent-skill", "agent-1", "", "user", "skill-1"] as const;
    const listed = skill("digest-list-d1");
    const detail = skill("digest-detail-d2");
    queryClient.setQueryData(queryKey, detail);

    expect(skillMutationDigest(listed, queryClient.getQueryData<Skill>(queryKey))).toBe(
      "digest-detail-d2",
    );

    await refreshSkillMutationBaseline(queryClient, queryKey, skill("digest-saved-d3"));

    expect(skillMutationDigest(listed, queryClient.getQueryData<Skill>(queryKey))).toBe(
      "digest-saved-d3",
    );
  });

  it("rejects a failed readback without advancing the cached baseline", async () => {
    const queryClient = new QueryClient();
    const queryKey = ["agent-skill", "agent-1", "", "user", "skill-1"] as const;
    const listed = skill("digest-list-d1");
    const detail = skill("digest-detail-d2");
    queryClient.setQueryDefaults(queryKey, {
      queryFn: async () => {
        throw new Error("detail readback failed");
      },
      retry: false,
    });
    queryClient.setQueryData(queryKey, detail);

    await expect(refreshSkillMutationBaseline(queryClient, queryKey)).rejects.toThrow(
      "detail readback failed",
    );
    expect(skillMutationDigest(listed, queryClient.getQueryData<Skill>(queryKey))).toBe(
      "digest-detail-d2",
    );
  });
});
