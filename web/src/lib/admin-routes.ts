const LEGACY_ADMIN_ROUTES: Array<[RegExp, string]> = [
  [/^\/settings\/providers(?:\/(.+))?$/, "/admin/ai/providers"],
  [/^\/settings\/embedding$/, "/admin/ai/embedding"],
  [/^\/settings\/vision$/, "/admin/ai/models"],
  [/^\/settings\/provisioning$/, "/admin/access/provisioning"],
  [/^\/settings\/users(?:\/(.+))?$/, "/admin/users"],
  // The list root is now personal MCP for every role. Only detail IDs remain
  // legacy deployment-plugin links and therefore move to Admin Console.
  [/^\/settings\/plugins\/(.+)$/, "/admin/integrations/plugins"],
  [/^\/settings\/about$/, "/admin/overview"],
];

/** Maps a legacy admin Settings URL to its Admin Console replacement. */
export function adminCompatibilityHref(pathname: string, search = ""): string | null {
  for (const [pattern, target] of LEGACY_ADMIN_ROUTES) {
    const match = pathname.match(pattern);
    if (!match) continue;
    const detail = match[1] ? `/${match[1]}` : "";
    return `${target}${detail}${search}`;
  }
  return null;
}

/** Keeps the former mixed Plugins root as an alias for personal MCP. */
export function personalCompatibilityHref(pathname: string, search = ""): string | null {
  return pathname === "/settings/plugins" ? `/settings/mcp${search}` : null;
}

/** Moves pre-split deployment Library bookmarks to the Admin surface. */
export function libraryCompatibilityHref(pathname: string, search = ""): string | null {
  if (pathname !== "/settings/library") return null;
  const scope = new URLSearchParams(search).get("scope");
  return scope === "system" || scope === "system_agent"
    ? `/admin/resources/library${search}`
    : null;
}
