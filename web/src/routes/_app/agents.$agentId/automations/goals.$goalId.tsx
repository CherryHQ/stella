import { createFileRoute } from "@tanstack/react-router";

interface GoalDetailSearch {
  task?: string;
}

export const Route = createFileRoute("/_app/agents/$agentId/automations/goals/$goalId")({
  validateSearch: (search: Record<string, unknown>): GoalDetailSearch => ({
    task: typeof search.task === "string" ? search.task : undefined,
  }),
});
