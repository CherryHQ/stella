import { createFileRoute } from "@tanstack/react-router";
import { isString, type RouteSearchInput } from "@/lib/route-search";

interface NewGoalSearch {
  project_id?: string;
}

export const Route = createFileRoute("/_app/agents/$agentId/goals/new")({
  validateSearch: (search: RouteSearchInput): NewGoalSearch => ({
    project_id: isString(search.project_id) ? search.project_id : undefined,
  }),
});
