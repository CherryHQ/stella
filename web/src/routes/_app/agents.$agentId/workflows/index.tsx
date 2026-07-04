import { createFileRoute, redirect } from "@tanstack/react-router";

// Workflows are a facet of the Goals surface, not a top-level tab; the old
// list URL lands on the Goals overview where the repeatable section lives.
export const Route = createFileRoute("/_app/agents/$agentId/workflows/")({
  beforeLoad: ({ params: { agentId } }) => {
    throw redirect({ to: "/agents/$agentId/goals", params: { agentId } });
  },
});
