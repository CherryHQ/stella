import { createFileRoute } from "@tanstack/react-router";
import { SchedulerPage } from "@/features/scheduler/SchedulerPage";

export const Route = createFileRoute("/_app/scheduler")({
  component: SchedulerPage,
});
