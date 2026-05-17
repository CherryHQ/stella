import { createLazyFileRoute } from "@tanstack/react-router";
import { SoulPage } from "@/features/sessions/pages/SoulPage";

export const Route = createLazyFileRoute("/_app/agents/$agentId/memories/soul")({
  component: SoulPage,
});
