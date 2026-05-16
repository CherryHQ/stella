import { useParams } from "@tanstack/react-router";
import { MemoryPanel } from "@/features/sessions/panels/MemoryPanel";

export function MemoryProfilePage() {
  const { agentId } = useParams({ from: "/_app/agents/$agentId/memories/profile" });

  return <MemoryPanel key={agentId} agentId={agentId} />;
}
