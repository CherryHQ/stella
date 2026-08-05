import { createFileRoute, redirect } from "@tanstack/react-router";

// The standalone skill editor is gone — a skill is inspected and edited in the
// profile's skills tab. Old deep links redirect there rather than 404.
export const Route = createFileRoute("/_app/agents/$agentId/skills/$skillId")({
  beforeLoad: ({ params: { agentId } }) => {
    throw redirect({
      to: "/agents/$agentId/profile",
      params: { agentId },
      search: { tab: "skills" as const },
      replace: true,
    });
  },
});
