/**
 * The agent you talked to last. Not navigable state — it only seeds the home
 * composer's default selection — so it lives in localStorage rather than the
 * URL, and a storage failure just means "no preference yet".
 */
const LAST_AGENT_KEY = "stella-last-agent";

export function readLastAgentId(): string {
  try {
    return window.localStorage.getItem(LAST_AGENT_KEY) ?? "";
  } catch {
    return "";
  }
}

export function writeLastAgentId(agentId: string) {
  if (!agentId) return;
  try {
    window.localStorage.setItem(LAST_AGENT_KEY, agentId);
  } catch {
    // Private-mode storage failures must not break navigation.
  }
}
