import { createFileRoute } from "@tanstack/react-router";

interface DeliverablesIndexSearch {
  new?: string;
  q?: string;
  project_id?: string;
}

export const Route = createFileRoute("/_app/agents/$agentId/deliverables/")({
  validateSearch: (search: Record<string, unknown>): DeliverablesIndexSearch => ({
    new: typeof search.new === "string" ? search.new : undefined,
    q: typeof search.q === "string" ? search.q : undefined,
    project_id: typeof search.project_id === "string" ? search.project_id : undefined,
  }),
});
