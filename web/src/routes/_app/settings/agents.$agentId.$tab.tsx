import { createFileRoute, redirect } from "@tanstack/react-router";

// The tab segment was never read by the settings detail panel; every variant
// now lands on the profile's config sections.
export const Route = createFileRoute("/_app/settings/agents/$agentId/$tab")({
  beforeLoad: ({ params: { agentId } }) => {
    throw redirect({
      to: "/agents/$agentId/profile",
      params: { agentId },
      search: { tab: "config" as const },
      replace: true,
    });
  },
});
