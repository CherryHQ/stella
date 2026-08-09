import type { Agent } from "@/lib/types";

/**
 * The agents a caller may bind a new channel to. can_manage is the server's own
 * answer — the channel API applies the same rule, so offering anything else
 * would only produce a failed create — and it keeps the client from having to
 * know who owns which agent.
 */
export function bindableAgents(agents: Agent[]): Agent[] {
  return agents.filter((agent) => agent.can_manage === true);
}
