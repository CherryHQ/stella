import { createFileRoute } from "@tanstack/react-router";
import { isString } from "@/lib/route-search";

interface NewGoalSearch {
  project_id?: string;
}

export const Route = createFileRoute("/_app/agents/$agentId/goals/new")({
  validateSearch: (search: Record<string, unknown>): NewGoalSearch => ({
    project_id: isString(search.project_id) ? search.project_id : undefined,
  }),
});
