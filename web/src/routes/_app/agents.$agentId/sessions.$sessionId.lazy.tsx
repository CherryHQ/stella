import { createLazyFileRoute, useParams } from "@tanstack/react-router";
import { SessionView } from "@/features/sessions/SessionView";

function SessionViewKeyed() {
  const { agentId, sessionId } = useParams({
    from: "/_app/agents/$agentId/sessions/$sessionId",
  });
  return <SessionView key={`${agentId}/${sessionId}`} />;
}

export const Route = createLazyFileRoute("/_app/agents/$agentId/sessions/$sessionId")({
  component: SessionViewKeyed,
});
