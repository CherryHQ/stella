import { createFileRoute } from "@tanstack/react-router";

interface NewGoalSearch {
  project_id?: string;
}

export const Route = createFileRoute("/_app/agents/$agentId/goals/new")({
  validateSearch: (search: Record<string, unknown>): NewGoalSearch => ({
    project_id: typeof search.project_id === "string" ? search.project_id : undefined,
  }),
});
