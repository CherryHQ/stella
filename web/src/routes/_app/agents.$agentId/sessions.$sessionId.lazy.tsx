import { createLazyFileRoute } from "@tanstack/react-router";
import { SessionView } from "@/features/sessions/SessionView";

export const Route = createLazyFileRoute("/_app/agents/$agentId/sessions/$sessionId")({
  component: SessionView,
});
