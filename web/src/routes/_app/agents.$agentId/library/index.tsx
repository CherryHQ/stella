import { createFileRoute, redirect } from "@tanstack/react-router";
import { isString, type RouteSearchInput } from "@/lib/route-search";

interface AgentLibrarySearch {
  q?: string;
}

// The library became a tab of the agent profile, like memory and skills before
// it; the old standalone URL keeps working as a redirect so existing links and
// bookmarks — including their file filter — land on that tab.
export const Route = createFileRoute("/_app/agents/$agentId/library/")({
  validateSearch: (search: RouteSearchInput): AgentLibrarySearch => ({
    q: isString(search.q) && search.q ? search.q.slice(0, 200) : undefined,
  }),
  beforeLoad: ({ params: { agentId }, search }) => {
    throw redirect({
      to: "/agents/$agentId/profile",
      params: { agentId },
      search: { tab: "library" as const, ...(search.q ? { q: search.q } : undefined) },
      replace: true,
    });
  },
});
