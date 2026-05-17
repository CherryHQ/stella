import { createLazyFileRoute } from "@tanstack/react-router";
import { MemoryProfilePage } from "@/features/sessions/pages/MemoryProfilePage";

export const Route = createLazyFileRoute("/_app/agents/$agentId/memories/profile")({
  component: MemoryProfilePage,
});
