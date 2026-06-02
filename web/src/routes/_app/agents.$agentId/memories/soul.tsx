import { createFileRoute, redirect } from "@tanstack/react-router";

export const Route = createFileRoute("/_app/agents/$agentId/memories/soul")({
  beforeLoad: ({ params }) => {
    throw redirect({ to: "/agents/$agentId/memories", params });
  },
});
