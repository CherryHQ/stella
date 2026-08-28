export interface MemorySearch {
  /** Search term used by the agent profile's Library tab. */
  q?: string;
  knowledge?: "removed";
  /**
   * Selected tab of the agent/project profile page. Absent means "overview" —
   * the landing tab is never written to the URL so a bare /profile link is
   * canonical.
   */
  tab?: ProfileTab;
}

export type ProfileTab =
  | "overview"
  | "memory"
  | "skills"
  | "library"
  | "tools"
  | "channels"
  | "config";

import type { JsonObject, JsonValue } from "./types";

export type RouteSearchInput = JsonObject;
export type SearchValue = JsonValue | undefined;

export function isString(value: SearchValue): value is string {
  return typeof value === "string";
}

export function isNumber(value: SearchValue): value is number {
  return typeof value === "number";
}

const PROFILE_TABS = new Set<string>([
  "overview",
  "memory",
  "skills",
  "library",
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

export function validateThreadsSearch(search: RouteSearchInput): ThreadsSearch {
  return {
    home: isString(search.home) && search.home ? search.home : undefined,
    q: isString(search.q) && search.q ? search.q : undefined,
  };
}

export function validateMemorySearch(search: RouteSearchInput): MemorySearch {
  const out: MemorySearch = {};
  if (isString(search.q) && search.q) out.q = search.q.slice(0, 200);
  if (search.knowledge === "removed") out.knowledge = "removed";
  // SAFETY: tab is validated by PROFILE_TABS before this cast.
  if (PROFILE_TABS.has(search.tab as string)) {
    // SAFETY: PROFILE_TABS membership was just validated above.
    out.tab = search.tab as ProfileTab;
  }
  return out;
}
