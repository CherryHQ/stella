import { createLazyFileRoute, useParams } from "@tanstack/react-router";
import { SessionView } from "@/features/sessions/SessionView";

function ProjectSessionViewKeyed() {
  const { agentId, projectId, sessionId } = useParams({
    from: "/_app/agents/$agentId/projects/$projectId/sessions/$sessionId",
  });
  return <SessionView key={`${agentId}/${projectId}/${sessionId}`} />;
}

export const Route = createLazyFileRoute(
  "/_app/agents/$agentId/projects/$projectId/sessions/$sessionId",
)({
  component: ProjectSessionViewKeyed,
});
