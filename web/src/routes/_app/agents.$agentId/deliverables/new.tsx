import { createFileRoute } from "@tanstack/react-router";

interface NewDeliverableSearch {
  project_id?: string;
}

export const Route = createFileRoute("/_app/agents/$agentId/deliverables/new")({
  validateSearch: (search: Record<string, unknown>): NewDeliverableSearch => ({
    project_id: typeof search.project_id === "string" ? search.project_id : undefined,
  }),
});
