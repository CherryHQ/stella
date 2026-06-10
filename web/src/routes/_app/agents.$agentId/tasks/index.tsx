import { createFileRoute } from "@tanstack/react-router";

interface TasksSearch {
  new?: string;
  q?: string;
}

export const Route = createFileRoute("/_app/agents/$agentId/tasks/")({
  validateSearch: (search: Record<string, unknown>): TasksSearch => ({
    new: typeof search.new === "string" ? search.new : undefined,
    q: typeof search.q === "string" ? search.q : undefined,
  }),
});
