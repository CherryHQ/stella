import { createFileRoute } from "@tanstack/react-router";
import { loadAgentsSettingsData } from "@/lib/queries/agent-settings";

export const Route = createFileRoute("/_app/settings/agents")({
  loader: () => loadAgentsSettingsData(),
});
