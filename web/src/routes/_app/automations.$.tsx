import { createFileRoute, redirect } from "@tanstack/react-router";

export const Route = createFileRoute("/_app/automations/$")({
  beforeLoad: () => {
    throw redirect({ to: "/agents" });
  },
});
