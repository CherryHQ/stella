import { createFileRoute } from "@tanstack/react-router";

interface GoalsSearch {
  view?: string;
  mode?: string;
}

export const Route = createFileRoute("/_app/agents/$agentId/tasks/goals")({
  validateSearch: (search: Record<string, unknown>): GoalsSearch => ({
    view: typeof search.view === "string" ? search.view : undefined,
    mode: typeof search.mode === "string" ? search.mode : undefined,
  }),
});
