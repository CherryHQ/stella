import { createFileRoute, redirect } from "@tanstack/react-router";

// The memory facet became a section of the agent profile page; the old URL is
// kept alive so bookmarks and agent-authored links never dead-end.
export const Route = createFileRoute("/_app/agents/$agentId/memories")({
  beforeLoad: ({ params }) => {
    throw redirect({ to: "/agents/$agentId/profile", params });
  },
});
