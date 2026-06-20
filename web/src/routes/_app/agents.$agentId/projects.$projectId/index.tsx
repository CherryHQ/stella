import { createFileRoute } from "@tanstack/react-router";

export type ProjectTab = "goals" | "sessions";

interface ProjectHomeSearch {
  tab?: ProjectTab;
  new?: string;
}

export const Route = createFileRoute("/_app/agents/$agentId/projects/$projectId/")({
  validateSearch: (search: Record<string, unknown>): ProjectHomeSearch => ({
    tab: search.tab === "goals" || search.tab === "sessions" ? search.tab : undefined,
    new: typeof search.new === "string" ? search.new : undefined,
  }),
});
