import { createFileRoute } from "@tanstack/react-router";
import { MemoryProfilePage } from "@/features/sessions/pages/MemoryProfilePage";

export const Route = createFileRoute("/_app/agents/$agentId/memories/profile")({
  component: MemoryProfilePage,
});
