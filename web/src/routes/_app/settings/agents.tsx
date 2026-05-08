import { createFileRoute } from "@tanstack/react-router";
import { AgentsPage } from "@/features/agents/AgentsPage";

export const Route = createFileRoute("/_app/settings/agents")({
  component: AgentsPage,
});
