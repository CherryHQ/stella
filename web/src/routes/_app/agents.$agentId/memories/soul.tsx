import { createFileRoute } from "@tanstack/react-router";
import { SoulPage } from "@/features/sessions/pages/SoulPage";

export const Route = createFileRoute("/_app/agents/$agentId/memories/soul")({
  component: SoulPage,
});
