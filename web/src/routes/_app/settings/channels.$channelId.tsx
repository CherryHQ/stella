import { createFileRoute } from "@tanstack/react-router";
import { isString, type RouteSearchInput } from "@/lib/route-search";

// No admin guard: the API scopes a non-admin to the channels of the agents they
// manage, so an unreachable id resolves to an empty detail pane.
export const Route = createFileRoute("/_app/settings/channels/$channelId")({
  // `agent` preselects the bound agent when creation starts from an agent's
  // profile, where the agent is already known.
  validateSearch: (search: RouteSearchInput): { agent?: string } =>
    isString(search.agent) && search.agent ? { agent: search.agent } : {},
});
