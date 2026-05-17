import { createLazyFileRoute } from "@tanstack/react-router";
import { AgentHome } from "@/features/sessions/AgentHome";

export const Route = createLazyFileRoute("/_app/agents/$agentId/")({
  component: AgentHome,
});
