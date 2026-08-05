import { createFileRoute } from "@tanstack/react-router";
import { validateThreadsSearch } from "@/lib/route-search";

export const Route = createFileRoute("/_app/agents/$agentId/threads")({
  validateSearch: validateThreadsSearch,
});
