function sameScopeSet(left: string[], right: string[]): boolean {
  const leftSet = new Set(left);
  const rightSet = new Set(right);
  return leftSet.size === rightSet.size && [...leftSet].every((scope) => rightSet.has(scope));
}

// The API uses [] to mean "inherit the manifest allowlist". When an admin
// narrows that list, persist at least the mandatory default-scope floor so an
// empty selection cannot accidentally restore the broader manifest policy.
export function buildOAuthAllowedScopeOverride(
  selected: string[],
  manifestAllowed: string[],
  defaultScopes: string[],
): string[] {
  return sameScopeSet(selected, manifestAllowed)
    ? []
    : Array.from(new Set([...defaultScopes, ...selected]));
}
