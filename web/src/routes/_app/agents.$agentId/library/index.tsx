import { createFileRoute, redirect } from "@tanstack/react-router";
import { isString, type RouteSearchInput } from "@/lib/route-search";

export interface AgentLibrarySearch {
  q?: string;
}

export const Route = createFileRoute("/_app/agents/$agentId/library/")({
  validateSearch: (search: RouteSearchInput): AgentLibrarySearch => ({
    q: isString(search.q) && search.q ? search.q.slice(0, 200) : undefined,
  }),
  beforeLoad: ({ params: { agentId }, search }) => {
    throw redirect({
      to: "/agents/$agentId/profile",
      params: { agentId },
      search: {
        tab: "library",
        ...(search.q ? { q: search.q } : undefined),
      },
      replace: true,
    });
  },
});
