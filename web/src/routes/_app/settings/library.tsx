import { createFileRoute } from "@tanstack/react-router";

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
});
