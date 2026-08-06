import { createFileRoute } from "@tanstack/react-router";
import { agentsQueryOptions } from "@/lib/queries/agents";

// The agents root is the app's front door now: it renders the home page
// instead of bouncing straight into some agent's conversation.
export const Route = createFileRoute("/_app/agents/")({
  loader: async ({ context: { queryClient } }) => {
    await queryClient.ensureQueryData(agentsQueryOptions);
  },
});
