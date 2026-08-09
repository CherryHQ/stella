import type { Agent } from "@/lib/types";

/**
 * The agents a caller may bind a new channel to: every agent for an admin, and
 * the agents they created for anyone else. The channel API applies the same rule
 * server-side, so offering more would only produce a failed create.
 */
export function bindableAgents(
  agents: Agent[],
  me: { id?: string; is_admin?: boolean } | undefined,
): Agent[] {
  if (me?.is_admin) return agents;
  if (!me?.id) return [];
  return agents.filter((agent) => !!agent.creator_id && agent.creator_id === me.id);
}
