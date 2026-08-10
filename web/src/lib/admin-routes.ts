const LEGACY_ADMIN_ROUTES: Array<[RegExp, string]> = [
  [/^\/settings\/providers(?:\/(.+))?$/, "/admin/ai/providers"],
  [/^\/settings\/embedding$/, "/admin/ai/embedding"],
  [/^\/settings\/vision$/, "/admin/ai/vision"],
  [/^\/settings\/provisioning$/, "/admin/access/provisioning"],
  [/^\/settings\/users(?:\/(.+))?$/, "/admin/users"],
  [/^\/settings\/plugins(?:\/(.+))?$/, "/admin/integrations/plugins"],
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
