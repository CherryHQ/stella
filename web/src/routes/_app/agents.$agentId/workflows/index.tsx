import { createFileRoute } from "@tanstack/react-router";
import { workflowsOptions } from "@/lib/queries/workflows";

export const Route = createFileRoute("/_app/agents/$agentId/workflows/")({
  loader: async ({ context: { queryClient }, params: { agentId } }) => {
    await queryClient.ensureQueryData(workflowsOptions(agentId));
  },
});
