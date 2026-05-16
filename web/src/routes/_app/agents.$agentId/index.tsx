import { createFileRoute } from "@tanstack/react-router";
import { AgentHome } from "@/features/sessions/AgentHome";

export const Route = createFileRoute("/_app/agents/$agentId/")({
  component: AgentHome,
});
