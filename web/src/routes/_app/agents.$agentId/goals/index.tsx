import { createFileRoute } from "@tanstack/react-router";

interface GoalsIndexSearch {
  new?: string;
  q?: string;
  project_id?: string;
}

export const Route = createFileRoute("/_app/agents/$agentId/goals/")({
  validateSearch: (search: Record<string, unknown>): GoalsIndexSearch => ({
    new: typeof search.new === "string" ? search.new : undefined,
    q: typeof search.q === "string" ? search.q : undefined,
    project_id: typeof search.project_id === "string" ? search.project_id : undefined,
  }),
});
