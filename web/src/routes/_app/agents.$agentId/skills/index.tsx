import { createFileRoute, redirect } from "@tanstack/react-router";

// An agent's skills live on its profile; the standalone skills page is gone.
// The old URL stays valid as a redirect so existing links and bookmarks land on
// the profile's skills tab.
export const Route = createFileRoute("/_app/agents/$agentId/skills/")({
  beforeLoad: ({ params: { agentId } }) => {
    throw redirect({
      to: "/agents/$agentId/profile",
      params: { agentId },
      search: { tab: "skills" as const },
      replace: true,
    });
  },
});
