import { queryOptions } from "@tanstack/react-query";
import { listAgentMcpServers } from "@/lib/api-client/sdk.gen";

/**
 * The MCP registrations effective for one agent, after the backend's
 * name-precedence dedup (user_agent > user > system_agent > system). Each row
 * carries `readable` (can the viewer manage it) and `shadowed_scopes` (which
 * same-named registrations lost), so the panel never re-resolves precedence
 * client-side — that duplicated the backend's rules and drifted once already.
 */
export function agentMcpServersOptions(agentId: string) {
  return queryOptions({
    queryKey: ["agent-mcp-servers", agentId],
    queryFn: async () => {
      const { data } = await listAgentMcpServers({
        path: { id: agentId },
        throwOnError: true,
      });
      // SAFETY: an empty MCP server list is a valid zero-valued result here.
      return data?.servers ?? [];
    },
    enabled: !!agentId,
  });
}
