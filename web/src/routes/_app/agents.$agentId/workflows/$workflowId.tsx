import { createFileRoute } from "@tanstack/react-router";
import { workflowOptions, workflowRunsOptions } from "@/lib/queries/workflows";

export const Route = createFileRoute("/_app/agents/$agentId/workflows/$workflowId")({
  loader: async ({ context: { queryClient }, params: { workflowId } }) => {
    await Promise.all([
      queryClient.ensureQueryData(workflowOptions(workflowId)),
      queryClient.ensureQueryData(workflowRunsOptions(workflowId, 10)),
    ]);
  },
});
