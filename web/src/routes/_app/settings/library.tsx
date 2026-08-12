import { createFileRoute, redirect } from "@tanstack/react-router";
import { libraryCompatibilityHref } from "@/lib/admin-routes";

export interface LibrarySettingsSearch {
  scope?: "system" | "system_agent";
  agent?: string;
  q?: string;
}

export const Route = createFileRoute("/_app/settings/library")({
  validateSearch: (search: Record<string, unknown>): LibrarySettingsSearch => ({
    scope: search.scope === "system" || search.scope === "system_agent" ? search.scope : undefined,
    agent: typeof search.agent === "string" && search.agent ? search.agent : undefined,
    q: typeof search.q === "string" && search.q ? search.q.slice(0, 200) : undefined,
  }),
  beforeLoad: ({ location }) => {
    const href = libraryCompatibilityHref(location.pathname, location.searchStr);
    if (href) throw redirect({ href, replace: true });
  },
});
