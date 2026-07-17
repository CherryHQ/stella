import { createFileRoute } from "@tanstack/react-router";
import { MemoriesPage } from "@/features/memories/MemoriesPage";
import { validateMemorySearch } from "@/lib/route-search";

export const Route = createFileRoute("/_app/agents/$agentId/projects/$projectId/memories")({
  validateSearch: validateMemorySearch,
  component: MemoriesPage,
});
