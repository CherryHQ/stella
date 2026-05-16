import { createFileRoute, redirect } from "@tanstack/react-router";

export const Route = createFileRoute("/_app/sessions")({
  beforeLoad: () => {
    throw redirect({ to: "/agents" });
  },
});
