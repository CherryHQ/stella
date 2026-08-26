import { queryOptions } from "@tanstack/react-query";
import { getAuthUser, listAuthUserAgents } from "@/lib/api-client";
import type { User } from "@/lib/types";
import { fetchAllAuthUsers } from "@/lib/auth-users";

export const authUsersQueryOptions = queryOptions({
  queryKey: ["auth-users"],
  queryFn: () => fetchAllAuthUsers(),
});

export function authUserDetailOptions(userId: string) {
  return queryOptions({
    queryKey: ["auth-user", userId],
    queryFn: async () => {
      const { data } = await getAuthUser({
        path: { id: userId },
        throwOnError: true,
      });
      // SAFETY: authUserAgents/getUser returns the requested User on success.
      return data as User;
    },
    enabled: !!userId,
  });
}

export function authUserAgentsOptions(userId: string) {
  return queryOptions({
    queryKey: ["auth-user-agents", userId],
    queryFn: async () => {
      const { data } = await listAuthUserAgents({
        path: { id: userId },
        throwOnError: true,
      });
      // SAFETY: fetchAllAuthUsers returns agent-id items under data.agent_ids.
      return (data?.agent_ids as string[]) ?? [];
    },
    enabled: !!userId,
  });
}
