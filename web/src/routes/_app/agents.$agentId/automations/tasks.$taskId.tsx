import { createFileRoute, redirect } from "@tanstack/react-router";

interface TaskSearch {
  q?: string;
}

export const Route = createFileRoute("/_app/agents/$agentId/automations/tasks/$taskId")({
  beforeLoad: ({ params, search }) => {
    throw redirect({
      to: "/agents/$agentId/tasks/$taskId",
      params,
      search,
    });
  },
  validateSearch: (search: Record<string, unknown>): TaskSearch => ({
    q: typeof search.q === "string" ? search.q : undefined,
  }),
});
