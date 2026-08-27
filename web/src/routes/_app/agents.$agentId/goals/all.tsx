import { createFileRoute } from "@tanstack/react-router";
import { isNumber, isString, type RouteSearchInput } from "@/lib/route-search";

interface GoalsListSearch {
  view?: string;
  mode?: string;
  status?: string;
  q?: string;
  workflow_id?: string;
  page?: number;
}

export const Route = createFileRoute("/_app/agents/$agentId/goals/all")({
  validateSearch: (search: RouteSearchInput): GoalsListSearch => ({
    view: isString(search.view) ? search.view : undefined,
    mode: isString(search.mode) ? search.mode : undefined,
    status: isString(search.status) ? search.status : undefined,
    q: isString(search.q) ? search.q : undefined,
    workflow_id: isString(search.workflow_id) ? search.workflow_id : undefined,
    page: isNumber(search.page) ? search.page : undefined,
  }),
});
