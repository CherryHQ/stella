import { createFileRoute, redirect } from "@tanstack/react-router";

export const Route = createFileRoute("/_app/tasks/$taskId")({
  beforeLoad: () => {
    throw redirect({ to: "/agents" });
  },
});
