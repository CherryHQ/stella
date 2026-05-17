import { createLazyFileRoute } from "@tanstack/react-router";
import { SchedulerPage } from "@/features/scheduler/SchedulerPage";

export const Route = createLazyFileRoute("/_app/scheduler")({
  component: SchedulerPage,
});
