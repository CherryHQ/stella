import { createFileRoute } from "@tanstack/react-router";
import { loadAgentsSettingsData } from "@/features/agents/AgentsPage";

export const Route = createFileRoute("/_app/settings/agents")({
  loader: () => loadAgentsSettingsData(),
});
