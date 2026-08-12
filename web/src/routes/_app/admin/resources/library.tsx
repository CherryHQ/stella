import { createFileRoute } from "@tanstack/react-router";

export interface AdminLibrarySearch {
  scope?: "system" | "system_agent";
  agent?: string;
  q?: string;
}

export const Route = createFileRoute("/_app/admin/resources/library")({
  validateSearch: (search: Record<string, unknown>): AdminLibrarySearch => ({
    scope: search.scope === "system_agent" ? "system_agent" : "system",
    agent: typeof search.agent === "string" && search.agent ? search.agent : undefined,
    q: typeof search.q === "string" && search.q ? search.q.slice(0, 200) : undefined,
  }),
});
