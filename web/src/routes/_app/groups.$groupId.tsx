import { createFileRoute } from "@tanstack/react-router";
import { agentsQueryOptions } from "@/lib/queries/agents";
import {
  groupQueryOptions,
  groupMembersQueryOptions,
  groupsQueryOptions,
} from "@/lib/queries/groups";

export const Route = createFileRoute("/_app/groups/$groupId")({
  loader: async ({ context: { queryClient }, params: { groupId } }) => {
    await Promise.all([
      queryClient.ensureQueryData(agentsQueryOptions),
      queryClient.ensureQueryData(groupsQueryOptions),
      queryClient.ensureQueryData(groupQueryOptions(groupId)),
      queryClient.ensureQueryData(groupMembersQueryOptions(groupId)),
    ]);
  },
});
