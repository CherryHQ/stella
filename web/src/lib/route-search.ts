export interface MemorySearch {
  knowledge?: "removed";
  /** Selected tab of the agent profile's configuration editor. */
  ctab?: AgentConfigTab;
}

export type AgentConfigTab = "config" | "prompt" | "tools" | "advanced" | "users";

const AGENT_CONFIG_TABS = new Set<string>(["config", "prompt", "tools", "advanced", "users"]);

export interface SkillsSearch {
  new?: boolean;
  source?: "installed" | "market" | "manual";
  fscope?: "project" | "user" | "agent" | "system";
  sel?: string;
}

const SKILL_SOURCES = new Set(["installed", "market", "manual"]);
const SKILL_SCOPES = new Set(["project", "user", "agent", "system"]);

export function validateMemorySearch(search: Record<string, unknown>): MemorySearch {
  return {
    ...(search.knowledge === "removed" ? { knowledge: "removed" as const } : {}),
    ...(AGENT_CONFIG_TABS.has(search.ctab as string)
      ? { ctab: search.ctab as AgentConfigTab }
      : {}),
  };
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
    sel: typeof search.sel === "string" ? search.sel : undefined,
  };
}
