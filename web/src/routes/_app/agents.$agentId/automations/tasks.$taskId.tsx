import { createFileRoute } from "@tanstack/react-router";

interface TaskSearch {
  q?: string;
}

export const Route = createFileRoute("/_app/agents/$agentId/automations/tasks/$taskId")({
  validateSearch: (search: Record<string, unknown>): TaskSearch => ({
    q: typeof search.q === "string" ? search.q : undefined,
  }),
});
