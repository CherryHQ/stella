import { createFileRoute } from "@tanstack/react-router";

interface SkillsSearch {
  new?: boolean;
  source?: "installed" | "market";
  fscope?: "project" | "user" | "agent" | "system";
  sel?: string;
}

const SOURCES = new Set(["installed", "market"]);
const SCOPES = new Set(["project", "user", "agent", "system"]);

export const Route = createFileRoute("/_app/agents/$agentId/projects/$projectId/skills/")({
  validateSearch: (search: Record<string, unknown>): SkillsSearch => ({
    new: search.new === true || search.new === "true",
    source: SOURCES.has(search.source as string)
      ? (search.source as SkillsSearch["source"])
      : undefined,
    fscope: SCOPES.has(search.fscope as string)
      ? (search.fscope as SkillsSearch["fscope"])
      : undefined,
    sel: typeof search.sel === "string" ? search.sel : undefined,
  }),
});
