import { createFileRoute } from "@tanstack/react-router";
import { TasksPage } from "@/features/tasks/TasksPage";

export const Route = createFileRoute("/_app/tasks/$taskId")({
  component: TaskRoute,
});

function TaskRoute() {
  const { taskId } = Route.useParams();
  return <TasksPage taskId={taskId} />;
}
