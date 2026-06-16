import { createFileRoute, redirect } from "@tanstack/react-router";

export const Route = createFileRoute("/_app/agents/$agentId/automations/new")({
  beforeLoad: ({ params }) => {
    throw redirect({
      to: "/agents/$agentId/tasks/new",
      params,
    });
  },
});
