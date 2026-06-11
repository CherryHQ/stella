import { createFileRoute, redirect } from "@tanstack/react-router";

export const Route = createFileRoute("/_app/agents/$agentId/projects/$projectId/tasks")({
  beforeLoad: ({ params }) => {
    throw redirect({
      to: "/agents/$agentId/projects/$projectId",
      params,
      search: { tab: "tasks" },
    });
  },
});
