import { createLazyFileRoute } from "@tanstack/react-router";
import { MemoriesPage } from "@/features/sessions/pages/MemoriesPage";

export const Route = createLazyFileRoute("/_app/agents/$agentId/memories/profile")({
  component: MemoriesPage,
});
