import { createFileRoute, redirect } from "@tanstack/react-router";

// Per-agent management lives on the agent's own profile; settings keeps the
// fleet inventory only. The old detail URL stays valid as a redirect so
// existing links and bookmarks land on the profile's config sections.
export const Route = createFileRoute("/_app/settings/agents/$agentId")({
  beforeLoad: ({ params: { agentId } }) => {
    throw redirect({
      to: "/agents/$agentId/profile",
      params: { agentId },
      search: { tab: "config" as const },
      replace: true,
    });
  },
});
