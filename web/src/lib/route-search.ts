export interface MemorySearch {
  knowledge?: "removed";
}

export interface SkillsSearch {
  new?: boolean;
  source?: "installed" | "removed" | "market" | "manual";
  fscope?: "project" | "user" | "agent" | "system";
  generated?: boolean;
  sel?: string;
}

const SKILL_SOURCES = new Set(["installed", "removed", "market", "manual"]);
const SKILL_SCOPES = new Set(["project", "user", "agent", "system"]);

export function validateMemorySearch(search: Record<string, unknown>): MemorySearch {
  return search.knowledge === "removed" ? { knowledge: "removed" } : {};
}

export function validateSkillsSearch(search: Record<string, unknown>): SkillsSearch {
  return {
    new: search.new === true || search.new === "true",
    source: SKILL_SOURCES.has(search.source as string)
      ? (search.source as SkillsSearch["source"])
      : undefined,
    fscope: SKILL_SCOPES.has(search.fscope as string)
      ? (search.fscope as SkillsSearch["fscope"])
      : undefined,
    generated: search.generated === true || search.generated === "true",
    sel: typeof search.sel === "string" ? search.sel : undefined,
  };
}
