import { createFileRoute, redirect } from "@tanstack/react-router";

interface AutomationsSearch {
  q?: string;
  view?: string;
}

export const Route = createFileRoute("/_app/agents/$agentId/automations/")({
  beforeLoad: ({ params, search }) => {
    if (search.view) {
      throw redirect({ to: "/agents/$agentId/tasks/goals", params, search });
    }
    throw redirect({ to: "/agents/$agentId/tasks", params, search });
  },
  validateSearch: (search: Record<string, unknown>): AutomationsSearch => ({
    q: typeof search.q === "string" ? search.q : undefined,
    view: typeof search.view === "string" ? search.view : undefined,
  }),
});
