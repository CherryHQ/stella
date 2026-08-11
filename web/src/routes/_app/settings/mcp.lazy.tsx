import { createLazyFileRoute } from "@tanstack/react-router";
import { PersonalMCPPage } from "@/features/mcp/MCPServersPage";

export const Route = createLazyFileRoute("/_app/settings/mcp")({
  component: PersonalMCPPage,
});
