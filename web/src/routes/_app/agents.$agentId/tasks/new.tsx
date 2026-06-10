import { createFileRoute } from "@tanstack/react-router";

interface TaskNewSearch {
  project_id?: string;
}

export const Route = createFileRoute("/_app/agents/$agentId/tasks/new")({
  validateSearch: (search: Record<string, unknown>): TaskNewSearch => ({
    project_id: typeof search.project_id === "string" ? search.project_id : undefined,
  }),
});
