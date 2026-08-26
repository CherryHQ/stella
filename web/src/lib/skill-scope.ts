import type { MessageKey } from "@/lib/i18n/messages";

// Every place a skill scope is named pulls its label from here, so a scope reads
// the same two-axis name (owner · range) everywhere instead of ad-hoc strings
// like "Shared" / "Built-in" that left users guessing.
export type SkillScope = "project" | "user" | "user_agent" | "system" | "system_agent";

export const SCOPE_LABEL_KEY: Record<SkillScope, MessageKey> = {
  user: "skills.scope.user.label",
  user_agent: "skills.scope.userAgent.label",
  system: "skills.scope.system.label",
  system_agent: "skills.scope.systemAgent.label",
  project: "skills.scope.project.label",
};

export const SCOPE_DESC_KEY: Record<SkillScope, MessageKey> = {
  user: "skills.scope.user.desc",
  user_agent: "skills.scope.userAgent.desc",
  system: "skills.scope.system.desc",
  system_agent: "skills.scope.systemAgent.desc",
  project: "skills.scope.project.desc",
};

// Scopes a skill can be installed/uploaded into. system_agent is admin-only and
// gated by the caller (showAgentScope).
export const INSTALL_SCOPES: Extract<SkillScope, "user" | "user_agent" | "system_agent">[] = [
  "user",
  "user_agent",
  "system_agent",
];

const WRITABLE = new Set<SkillScope>(["user", "user_agent", "system", "system_agent"]);

// SAFETY: scope strings are validated against SCOPE_LABEL_KEY membership, then
// narrowed to a SkillScope key for the i18n lookup; unknown scopes return undefined.
export function skillScopeLabelKey(scope: string): MessageKey | undefined {
  return scope in SCOPE_LABEL_KEY ? SCOPE_LABEL_KEY[scope as SkillScope] : undefined;
}

// SAFETY: same membership check before narrowing to the description-key index.
export function skillScopeDescKey(scope: string): MessageKey | undefined {
  return scope in SCOPE_DESC_KEY ? SCOPE_DESC_KEY[scope as SkillScope] : undefined;
}

// Mirror the backend write rules so the UI never offers an edit that 403s:
// system scopes are admin-managed; project skills stay filesystem-owned.
export function isSkillReadOnly(scope: string, isAdmin: boolean): boolean {
  if (scope === "system" || scope === "system_agent") return !isAdmin;
  // SAFETY: WRITABLE only contains SkillScope values, so the lookup is safe.
  return !WRITABLE.has(scope as SkillScope);
}
