import { createFileRoute } from "@tanstack/react-router";
import { isString } from "@/lib/route-search";

export interface AgentLibrarySearch {
  q?: string;
}

export const Route = createFileRoute("/_app/agents/$agentId/library/")({
  validateSearch: (search: Record<string, unknown>): AgentLibrarySearch => ({
    q: isString(search.q) && search.q ? search.q.slice(0, 200) : undefined,
  }),
});
