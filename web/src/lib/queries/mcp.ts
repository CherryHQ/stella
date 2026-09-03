import { infiniteQueryOptions, queryOptions } from "@tanstack/react-query";
import { listAgentMcpServers, listMcpRegistryServers } from "@/lib/api-client/sdk.gen";

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

/**
 * Marketplace catalog pages for one search string. The backend paginates
 * upstream on our behalf; `next_page_token` is opaque and feeds straight back
 * as `page_token`.
 */
export function mcpRegistryInfiniteQueryOptions(q: string) {
  return infiniteQueryOptions({
    queryKey: ["mcp-registry", q],
    initialPageParam: "",
    queryFn: async ({ pageParam }) => {
      const { data } = await listMcpRegistryServers({
        query: { q: q || undefined, page_size: 20, page_token: pageParam || undefined },
        throwOnError: true,
      });
      return data ?? { servers: [], next_page_token: null };
    },
    getNextPageParam: (last) => last.next_page_token ?? undefined,
  });
}
