import { createFileRoute } from "@tanstack/react-router";

interface AutomationsSearch {
  q?: string;
}

export const Route = createFileRoute("/_app/agents/$agentId/automations/")({
  validateSearch: (search: Record<string, unknown>): AutomationsSearch => ({
    q: typeof search.q === "string" ? search.q : undefined,
  }),
});
