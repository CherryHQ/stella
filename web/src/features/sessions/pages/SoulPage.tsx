import { useParams } from "@tanstack/react-router";
import { SoulPanel } from "@/features/sessions/panels/SoulPanel";

export function SoulPage() {
  const { agentId } = useParams({ from: "/_app/agents/$agentId/memories/soul" });

  return <SoulPanel key={agentId} agentId={agentId} />;
}
