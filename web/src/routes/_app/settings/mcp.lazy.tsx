import { createLazyFileRoute } from "@tanstack/react-router";
import { MCPServersPage } from "@/features/mcp/MCPServersPage";

export const Route = createLazyFileRoute("/_app/settings/mcp")({
  component: MCPServersPage,
});
