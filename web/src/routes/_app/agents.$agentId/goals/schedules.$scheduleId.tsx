import { createFileRoute } from "@tanstack/react-router";
import { isString } from "@/lib/route-search";

interface ScheduleSearch {
  q?: string;
}

export const Route = createFileRoute("/_app/agents/$agentId/goals/schedules/$scheduleId")({
  validateSearch: (search: Record<string, unknown>): ScheduleSearch => ({
    q: isString(search.q) ? search.q : undefined,
  }),
});
