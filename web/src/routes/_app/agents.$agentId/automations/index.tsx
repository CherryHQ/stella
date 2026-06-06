import { createFileRoute } from "@tanstack/react-router";

interface AutomationsSearch {
  item?: string;
  q?: string;
}

export const Route = createFileRoute("/_app/agents/$agentId/automations/")({
  validateSearch: (search: Record<string, unknown>): AutomationsSearch => ({
    item: typeof search.item === "string" ? search.item : undefined,
    q: typeof search.q === "string" ? search.q : undefined,
  }),
});
