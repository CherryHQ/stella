import { createLazyFileRoute } from "@tanstack/react-router";
import { AgentsPage } from "@/features/agents/AgentsPage";

export const Route = createLazyFileRoute("/_app/settings/agents/$agentId/$tab")({
  component: AgentsPage,
});
