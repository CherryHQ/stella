import { createFileRoute, redirect } from "@tanstack/react-router";

// See agents.$agentId/memories.tsx — memory now lives on the profile page.
export const Route = createFileRoute("/_app/agents/$agentId/projects/$projectId/memories")({
  beforeLoad: ({ params }) => {
    throw redirect({ to: "/agents/$agentId/projects/$projectId/profile", params });
  },
});
