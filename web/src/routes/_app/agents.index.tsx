import { createFileRoute, redirect } from "@tanstack/react-router";
import { agentsQueryOptions } from "@/lib/queries/agents";

export const Route = createFileRoute("/_app/agents/")({
  beforeLoad: async ({ context: { queryClient } }) => {
    const agents = await queryClient.ensureQueryData(agentsQueryOptions);
    if (agents.length > 0) {
      throw redirect({ to: "/agents/$agentId", params: { agentId: agents[0].id } });
    }
  },
});
