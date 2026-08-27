import { queryOptions } from "@tanstack/react-query";
import { listGroups, getGroup, listGroupMembers } from "@/lib/api-client/sdk.gen";
import type { Group, GroupMember } from "@/lib/api-client/types.gen";

export const groupsQueryOptions = queryOptions({
  queryKey: ["groups"],
  queryFn: async () => {
    const { data } = await listGroups({ throwOnError: true });
    // SAFETY: listGroups returns group items under data.groups.
    return (data?.groups ?? []) as Group[];
  },
});

export function groupQueryOptions(groupId: string) {
  return queryOptions({
    queryKey: ["group", groupId],
    queryFn: async () => {
      const { data } = await getGroup({
        path: { groupId },
        throwOnError: true,
      });
      // SAFETY: getGroup returns the requested Group on success.
      return data as Group;
    },
    enabled: !!groupId,
  });
}

export function groupMembersQueryOptions(groupId: string) {
  return queryOptions({
    queryKey: ["group-members", groupId],
    queryFn: async () => {
      const { data } = await listGroupMembers({
        path: { groupId },
        throwOnError: true,
      });
      // SAFETY: listGroupMembers returns member items under data.members.
      return (data?.members ?? []) as GroupMember[];
    },
    enabled: !!groupId,
  });
}
