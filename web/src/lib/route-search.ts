export interface MemorySearch {
  knowledge?: "removed";
  /**
   * Selected tab of the agent/project profile page. Absent means "overview" —
   * the landing tab is never written to the URL so a bare /profile link is
   * canonical.
   */
  tab?: ProfileTab;
}

export type ProfileTab = "overview" | "memory" | "skills" | "tools" | "channels" | "config";

const PROFILE_TABS = new Set<string>([
  "overview",
  "memory",
  "skills",
  "tools",
  "channels",
  "config",
]);

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
  const out: MemorySearch = {};
  if (search.knowledge === "removed") out.knowledge = "removed";
  // SAFETY: tab is validated by PROFILE_TABS before this cast.
  if (PROFILE_TABS.has(search.tab as string)) {
    // SAFETY: PROFILE_TABS membership was just validated above.
    out.tab = search.tab as ProfileTab;
  }
  return out;
}
