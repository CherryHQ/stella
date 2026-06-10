import { createLazyFileRoute, useParams } from "@tanstack/react-router";
import { AutomationsPage } from "@/features/automations/AutomationsPage";

export const Route = createLazyFileRoute("/_app/agents/$agentId/tasks/schedules/$scheduleId")({
  component: ScheduleKeyed,
});

function ScheduleKeyed() {
  const { agentId, scheduleId } = useParams({
    from: "/_app/agents/$agentId/tasks/schedules/$scheduleId",
  });
  return (
    <AutomationsPage
      key={`${agentId}/schedule/${scheduleId}`}
      selectedKind="schedule"
      selectedId={scheduleId}
    />
  );
}
