export type ScopeBand = "personal" | "system";

export type ManagedScope = "user" | "user_agent" | "system" | "system_agent";

const SCOPES_BY_BAND = {
  personal: ["user", "user_agent"],
  system: ["system", "system_agent"],
} as const satisfies Record<ScopeBand, readonly ManagedScope[]>;

export function scopesForBand(scopeBand: ScopeBand): readonly ManagedScope[] {
  return SCOPES_BY_BAND[scopeBand];
}

export function scopeForRange(scopeBand: ScopeBand, agentSpecific: boolean): ManagedScope {
  if (scopeBand === "system") return agentSpecific ? "system_agent" : "system";
  return agentSpecific ? "user_agent" : "user";
}

export function isAgentManagedScope(scope: ManagedScope): boolean {
  return scope === "user_agent" || scope === "system_agent";
}

export function scopeQueriesForBand(
  scopeBand: ScopeBand,
  agentIDs: readonly string[],
): Array<{ scope: ManagedScope; agentID?: string }> {
  const [globalScope, agentScope] = scopesForBand(scopeBand);
  return [{ scope: globalScope }, ...agentIDs.map((agentID) => ({ scope: agentScope, agentID }))];
}
