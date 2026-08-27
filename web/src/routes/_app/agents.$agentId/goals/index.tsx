import { createFileRoute } from "@tanstack/react-router";
import { isString, type RouteSearchInput } from "@/lib/route-search";

interface GoalsIndexSearch {
  new?: string;
  q?: string;
  project_id?: string;
}

export const Route = createFileRoute("/_app/agents/$agentId/goals/")({
  validateSearch: (search: RouteSearchInput): GoalsIndexSearch => ({
    new: isString(search.new) ? search.new : undefined,
    q: isString(search.q) ? search.q : undefined,
    project_id: isString(search.project_id) ? search.project_id : undefined,
  }),
});
