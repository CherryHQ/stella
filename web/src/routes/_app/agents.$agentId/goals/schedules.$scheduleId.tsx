import { createFileRoute } from "@tanstack/react-router";
import { isString, type RouteSearchInput } from "@/lib/route-search";

interface ScheduleSearch {
  q?: string;
}

export const Route = createFileRoute("/_app/agents/$agentId/goals/schedules/$scheduleId")({
  validateSearch: (search: RouteSearchInput): ScheduleSearch => ({
    q: isString(search.q) ? search.q : undefined,
  }),
});
