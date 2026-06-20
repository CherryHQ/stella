import { createFileRoute } from "@tanstack/react-router";

interface GoalSearch {
  tab?: string;
}

export const Route = createFileRoute("/_app/agents/$agentId/goals/$goalId")({
  validateSearch: (search: Record<string, unknown>): GoalSearch => ({
    tab: typeof search.tab === "string" ? search.tab : undefined,
  }),
});
