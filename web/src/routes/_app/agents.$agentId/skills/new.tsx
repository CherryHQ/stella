import { createFileRoute } from "@tanstack/react-router";

interface SkillNewSearch {
  tab?: "catalog" | "upload";
}

export const Route = createFileRoute("/_app/agents/$agentId/skills/new")({
  validateSearch: (search: Record<string, unknown>): SkillNewSearch => ({
    tab: search.tab === "upload" || search.tab === "catalog" ? search.tab : undefined,
  }),
});
