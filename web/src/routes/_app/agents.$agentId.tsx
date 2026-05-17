import { createFileRoute } from "@tanstack/react-router";
import {
  agentsQueryOptions,
  agentSchedulerJobsOptions,
  agentSkillsOptions,
  agentMemoriesOptions,
} from "@/lib/queries/agents";

export const Route = createFileRoute("/_app/agents/$agentId")({
  loader: async ({ context: { queryClient }, params: { agentId } }) => {
    await Promise.all([
      queryClient.ensureQueryData(agentsQueryOptions),
      queryClient.ensureQueryData(agentSchedulerJobsOptions(agentId)),
      queryClient.ensureQueryData(agentSkillsOptions(agentId)),
      queryClient.ensureQueryData(agentMemoriesOptions(agentId)),
    ]);
  },
});
