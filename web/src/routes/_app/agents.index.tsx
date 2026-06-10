import { createFileRoute, redirect } from "@tanstack/react-router";
import { agentsQueryOptions } from "@/lib/queries/agents";

export const Route = createFileRoute("/_app/agents/")({
  beforeLoad: async ({ context: { queryClient } }) => {
    const agents = await queryClient.ensureQueryData(agentsQueryOptions);
    const defaultAgent = agents.find((agent) => agent.name.toLowerCase() === "stella") ?? agents[0];
    if (defaultAgent) {
      throw redirect({ to: "/agents/$agentId", params: { agentId: defaultAgent.id } });
    }
  },
});
