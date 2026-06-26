import { createFileRoute } from "@tanstack/react-router";

interface WorkflowsSearch {
  q?: string;
  page?: number;
}

export const Route = createFileRoute("/_app/agents/$agentId/workflows/")({
  validateSearch: (search: Record<string, unknown>): WorkflowsSearch => ({
    q: typeof search.q === "string" ? search.q : undefined,
    page: typeof search.page === "number" ? search.page : undefined,
  }),
});
