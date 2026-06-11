import { createFileRoute } from "@tanstack/react-router";

export type ProjectTab = "tasks" | "sessions" | "files";

interface ProjectHomeSearch {
  tab?: ProjectTab;
}

export const Route = createFileRoute("/_app/agents/$agentId/projects/$projectId/")({
  validateSearch: (search: Record<string, unknown>): ProjectHomeSearch => ({
    tab: search.tab === "sessions" || search.tab === "files" ? search.tab : undefined,
  }),
});
