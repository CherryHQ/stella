import { createFileRoute } from "@tanstack/react-router";

interface SkillsSearch {
  new?: boolean;
  expand?: string;
  scope?: string;
}

export const Route = createFileRoute("/_app/agents/$agentId/projects/$projectId/skills/")({
  validateSearch: (search: Record<string, unknown>): SkillsSearch => ({
    new: search.new === true || search.new === "true",
    expand: typeof search.expand === "string" ? search.expand : undefined,
    scope: typeof search.scope === "string" ? search.scope : undefined,
  }),
});
