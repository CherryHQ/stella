import { createFileRoute } from "@tanstack/react-router";

interface GoalsSearch {
  view?: "triage" | "board" | "table";
}

export const Route = createFileRoute("/_app/agents/$agentId/automations/")({
  validateSearch: (search: Record<string, unknown>): GoalsSearch => ({
    view:
      search.view === "board" || search.view === "table" || search.view === "triage"
        ? search.view
        : undefined,
  }),
});
