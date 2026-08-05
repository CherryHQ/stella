import { createFileRoute } from "@tanstack/react-router";
import { validateMemorySearch } from "@/lib/route-search";

export const Route = createFileRoute("/_app/agents/$agentId/projects/$projectId/profile")({
  validateSearch: validateMemorySearch,
});
