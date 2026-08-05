import { createFileRoute, redirect } from "@tanstack/react-router";

// Project skills live on the project's profile; the standalone skills page is
// gone. The old URL stays valid as a redirect for existing links and bookmarks.
export const Route = createFileRoute("/_app/agents/$agentId/projects/$projectId/skills/")({
  beforeLoad: ({ params: { agentId, projectId } }) => {
    throw redirect({
      to: "/agents/$agentId/projects/$projectId/profile",
      params: { agentId, projectId },
      search: { tab: "skills" as const },
      replace: true,
    });
  },
});
