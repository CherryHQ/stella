import { createFileRoute } from "@tanstack/react-router";
import { SessionView } from "@/features/sessions/SessionView";

export const Route = createFileRoute("/_app/agents/$agentId/sessions/$sessionId")({
  component: SessionView,
});
