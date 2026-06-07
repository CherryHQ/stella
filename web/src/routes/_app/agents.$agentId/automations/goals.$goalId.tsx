import { createFileRoute } from "@tanstack/react-router";

interface GoalSearch {
  q?: string;
  task?: string;
}

export const Route = createFileRoute("/_app/agents/$agentId/automations/goals/$goalId")({
  validateSearch: (search: Record<string, unknown>): GoalSearch => ({
    q: typeof search.q === "string" ? search.q : undefined,
    task: typeof search.task === "string" ? search.task : undefined,
  }),
});
