import { createFileRoute, redirect } from "@tanstack/react-router";
import { meQueryOptions } from "@/lib/queries/me";

export type SettingsKnowledgeScope = "user" | "system" | "system_agent";

interface SettingsKnowledgeSearch {
  scope: SettingsKnowledgeScope;
  agent_id?: string;
  q?: string;
}

export const Route = createFileRoute("/_app/settings/knowledge")({
  validateSearch: (search: Record<string, unknown>): SettingsKnowledgeSearch => ({
    scope: search.scope === "system" || search.scope === "system_agent" ? search.scope : "user",
    agent_id: typeof search.agent_id === "string" && search.agent_id ? search.agent_id : undefined,
    q: typeof search.q === "string" && search.q ? search.q.slice(0, 200) : undefined,
  }),
  beforeLoad: async ({ context: { queryClient }, search }) => {
    const me = await queryClient.ensureQueryData(meQueryOptions);

    // URL parameters are untrusted. A regular user always lands on their
    // personal scope, even when a system scope is typed into the address bar.
    if (!me?.is_admin && (search.scope !== "user" || search.agent_id)) {
      throw redirect({
        to: "/settings/knowledge",
        search: { scope: "user", q: search.q },
        replace: true,
      });
    }

    // agent_id has meaning only for system_agent. Canonicalizing it here keeps
    // stale target IDs from leaking across scope switches or shared URLs.
    if (search.scope !== "system_agent" && search.agent_id) {
      throw redirect({
        to: "/settings/knowledge",
        search: { scope: search.scope, q: search.q },
        replace: true,
      });
    }
  },
});
