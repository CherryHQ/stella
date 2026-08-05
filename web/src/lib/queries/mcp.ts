import { queryOptions } from "@tanstack/react-query";
import { listScopedMcpServers } from "@/lib/api-client/sdk.gen";
import type { McpServer } from "@/lib/api-client/types.gen";

type McpScope = McpServer["scope"];

/**
 * Most specific first — the same precedence the backend applies when it dedupes
 * registrations by name for an agent (`ListMCPServersForAgentContext`). A UI
 * that resolves a name to a server must pick the same winner, or an edit would
 * land on a registration the agent isn't actually using.
 */
export const MCP_SCOPE_PRECEDENCE: McpScope[] = ["user_agent", "user", "system_agent", "system"];

/**
 * Every MCP registration the viewer may *read* for one agent. The vault gates
 * the system scopes to admins (`ResolveScope`), so a non-admin never asks for
 * them: the request would 403 and the row it would explain stays read-only.
 */
export function agentMcpServersOptions(agentId: string, isAdmin: boolean) {
  return queryOptions({
    queryKey: ["agent-mcp-servers", agentId, isAdmin],
    queryFn: async () => {
      const targets: { scope: McpScope; agent_id?: string }[] = [
        { scope: "user" },
        { scope: "user_agent", agent_id: agentId },
        ...(isAdmin
          ? ([{ scope: "system" }, { scope: "system_agent", agent_id: agentId }] as const)
          : []),
      ];
      const results = await Promise.all(
        targets.map(async (query) => {
          try {
            const { data } = await listScopedMcpServers({ query, throwOnError: true });
            return data?.servers ?? [];
          } catch {
            // One unreadable scope must not blank the whole list: the rows it
            // would have matched simply stay unmanageable.
            return [] as McpServer[];
          }
        }),
      );
      return results.flat();
    },
    enabled: !!agentId,
  });
}
