import { createFileRoute } from "@tanstack/react-router";

interface SkillsSearch {
  new?: boolean;
  source?: "installed" | "market";
  fscope?: "project" | "user" | "user_agent" | "system_agent" | "system";
  sel?: string;
}

const SOURCES = new Set(["installed", "market"]);
const SCOPES = new Set(["project", "user", "user_agent", "system_agent", "system"]);

export const Route = createFileRoute("/_app/agents/$agentId/skills/")({
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
