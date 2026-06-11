import { createFileRoute } from "@tanstack/react-router";
import { MemoriesPage } from "@/features/memories/MemoriesPage";

export const Route = createFileRoute("/_app/agents/$agentId/projects/$projectId/memories")({
  component: MemoriesPage,
});
