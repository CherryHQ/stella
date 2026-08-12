import { createFileRoute } from "@tanstack/react-router";

export interface AgentLibrarySearch {
  q?: string;
}

export const Route = createFileRoute("/_app/agents/$agentId/library/")({
  validateSearch: (search: Record<string, unknown>): AgentLibrarySearch => ({
    q: typeof search.q === "string" && search.q ? search.q.slice(0, 200) : undefined,
  }),
});
