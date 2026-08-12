import { createLazyFileRoute } from "@tanstack/react-router";
import { GlobalMCPPage } from "@/features/mcp/MCPServersPage";

export const Route = createLazyFileRoute("/_app/admin/resources/mcp")({
  component: GlobalMCPPage,
});
