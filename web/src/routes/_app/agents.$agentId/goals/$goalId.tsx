import { createFileRoute } from "@tanstack/react-router";
import { isString, type RouteSearchInput } from "@/lib/route-search";

interface GoalSearch {
  tab?: string;
  node?: string;
}

export const Route = createFileRoute("/_app/agents/$agentId/goals/$goalId")({
  validateSearch: (search: RouteSearchInput): GoalSearch => ({
    tab: isString(search.tab) ? search.tab : undefined,
    node: isString(search.node) ? search.node : undefined,
  }),
});
