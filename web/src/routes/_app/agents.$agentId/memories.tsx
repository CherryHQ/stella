import { createFileRoute } from "@tanstack/react-router";
import { MemoriesPage } from "@/features/memories/MemoriesPage";

export const Route = createFileRoute("/_app/agents/$agentId/memories")({
  component: MemoriesPage,
});
