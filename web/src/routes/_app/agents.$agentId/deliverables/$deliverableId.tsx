import { createFileRoute } from "@tanstack/react-router";

interface DeliverableSearch {
  tab?: string;
}

export const Route = createFileRoute("/_app/agents/$agentId/deliverables/$deliverableId")({
  validateSearch: (search: Record<string, unknown>): DeliverableSearch => ({
    tab: typeof search.tab === "string" ? search.tab : undefined,
  }),
});
