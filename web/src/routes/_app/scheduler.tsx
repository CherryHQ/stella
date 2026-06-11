import { createFileRoute, redirect } from "@tanstack/react-router";

export const Route = createFileRoute("/_app/scheduler")({
  beforeLoad: () => {
    throw redirect({ to: "/agents" });
  },
});
