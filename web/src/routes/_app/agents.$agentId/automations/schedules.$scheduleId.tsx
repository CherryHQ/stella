import { createFileRoute } from "@tanstack/react-router";

interface ScheduleSearch {
  q?: string;
}

export const Route = createFileRoute("/_app/agents/$agentId/automations/schedules/$scheduleId")({
  validateSearch: (search: Record<string, unknown>): ScheduleSearch => ({
    q: typeof search.q === "string" ? search.q : undefined,
  }),
});
