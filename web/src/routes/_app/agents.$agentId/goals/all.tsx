import { createFileRoute } from "@tanstack/react-router";

interface GoalsListSearch {
  view?: string;
  mode?: string;
  status?: string;
  q?: string;
  workflow_id?: string;
  page?: number;
}

export const Route = createFileRoute("/_app/agents/$agentId/goals/all")({
  validateSearch: (search: Record<string, unknown>): GoalsListSearch => ({
    view: typeof search.view === "string" ? search.view : undefined,
    mode: typeof search.mode === "string" ? search.mode : undefined,
    status: typeof search.status === "string" ? search.status : undefined,
    q: typeof search.q === "string" ? search.q : undefined,
    workflow_id: typeof search.workflow_id === "string" ? search.workflow_id : undefined,
    page: typeof search.page === "number" ? search.page : undefined,
  }),
});
