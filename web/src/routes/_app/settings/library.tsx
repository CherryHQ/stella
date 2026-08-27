import { createFileRoute, redirect } from "@tanstack/react-router";
import { libraryCompatibilityHref } from "@/lib/admin-routes";
import { isString, type RouteSearchInput } from "@/lib/route-search";

export interface LibrarySettingsSearch {
  scope?: "system" | "system_agent";
  agent?: string;
  q?: string;
}

export const Route = createFileRoute("/_app/settings/library")({
  validateSearch: (search: RouteSearchInput): LibrarySettingsSearch => ({
    scope: search.scope === "system" || search.scope === "system_agent" ? search.scope : undefined,
    agent: isString(search.agent) && search.agent ? search.agent : undefined,
    q: isString(search.q) && search.q ? search.q.slice(0, 200) : undefined,
  }),
  beforeLoad: ({ location }) => {
    const href = libraryCompatibilityHref(location.pathname, location.searchStr);
    if (href) throw redirect({ href, replace: true });
  },
});
