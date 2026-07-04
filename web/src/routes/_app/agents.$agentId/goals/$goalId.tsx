import { createFileRoute } from "@tanstack/react-router";

interface GoalSearch {
  tab?: string;
  node?: string;
}

export const Route = createFileRoute("/_app/agents/$agentId/goals/$goalId")({
  validateSearch: (search: Record<string, unknown>): GoalSearch => ({
    tab: typeof search.tab === "string" ? search.tab : undefined,
    node: typeof search.node === "string" ? search.node : undefined,
  }),
});
