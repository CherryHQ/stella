import { createFileRoute, redirect } from "@tanstack/react-router";

export const Route = createFileRoute("/_app/agents/$agentId/automations/$jobId")({
  beforeLoad: ({ params }) => {
    throw redirect({
      to: "/agents/$agentId/tasks/schedules/$scheduleId",
      params: { agentId: params.agentId, scheduleId: params.jobId },
    });
  },
});
