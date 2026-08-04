export interface MemorySearch {
  knowledge?: "removed";
  /**
   * Selected tab of the agent/project profile page. Absent means "overview" —
   * the landing tab is never written to the URL so a bare /profile link is
   * canonical.
   */
  tab?: ProfileTab;
  /**
   * Selected sub-tab *inside* the profile's configuration editor. It stays a
   * separate param rather than a compound `tab` value so the outer tab strip
   * and the embedded editor each own exactly one key.
   */
  ctab?: AgentConfigTab;
}

export type ProfileTab = "overview" | "memory" | "skills" | "tools" | "config";

export type AgentConfigTab = "config" | "prompt" | "tools" | "advanced" | "users";

const PROFILE_TABS = new Set<string>(["overview", "memory", "skills", "tools", "config"]);

const AGENT_CONFIG_TABS = new Set<string>(["config", "prompt", "tools", "advanced", "users"]);

export interface SkillsSearch {
  new?: boolean;
  source?: "installed" | "market" | "manual";
  fscope?: "project" | "user" | "agent" | "system";
  sel?: string;
}

const SKILL_SOURCES = new Set(["installed", "market", "manual"]);
const SKILL_SCOPES = new Set(["project", "user", "agent", "system"]);

export interface ThreadsSearch {
  /**
   * Which home the thread list is scoped to: absent means every home, "agent"
   * means agent-level threads only, anything else is a project id. Unknown ids
   * are harmless — the page falls back to "all" when it cannot resolve one.
   */
  home?: string;
  /** Client-side title filter over the pages loaded so far. */
  q?: string;
}

export function validateThreadsSearch(search: Record<string, unknown>): ThreadsSearch {
  return {
    home: typeof search.home === "string" && search.home ? search.home : undefined,
    q: typeof search.q === "string" && search.q ? search.q : undefined,
  };
}

export function validateMemorySearch(search: Record<string, unknown>): MemorySearch {
  return {
    ...(search.knowledge === "removed" ? { knowledge: "removed" as const } : {}),
    ...(PROFILE_TABS.has(search.tab as string) ? { tab: search.tab as ProfileTab } : {}),
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
