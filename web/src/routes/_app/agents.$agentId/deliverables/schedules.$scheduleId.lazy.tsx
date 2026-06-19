import { createLazyFileRoute, useParams } from "@tanstack/react-router";
import { SchedulePage } from "@/features/deliverables/SchedulePage";

export const Route = createLazyFileRoute(
  "/_app/agents/$agentId/deliverables/schedules/$scheduleId",
)({
  component: ScheduleKeyed,
});

function ScheduleKeyed() {
  const { agentId, scheduleId } = useParams({
    from: "/_app/agents/$agentId/deliverables/schedules/$scheduleId",
  });
  return <SchedulePage key={`${agentId}/schedule/${scheduleId}`} />;
}
