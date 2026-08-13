import type { QueryClient } from "@tanstack/react-query";
import type { Skill } from "@/lib/types";

export function skillMutationDigest(
  skill: Pick<Skill, "content_digest">,
  detail?: Pick<Skill, "content_digest">,
) {
  return detail?.content_digest ?? skill.content_digest;
}

export async function refreshSkillMutationBaseline(
  queryClient: QueryClient,
  queryKey: readonly unknown[],
  updated?: Skill,
) {
  if (updated?.content_digest) {
    queryClient.setQueryData(queryKey, updated);
    return;
  }
  await queryClient.refetchQueries({ queryKey, exact: true }, { throwOnError: true });
}
