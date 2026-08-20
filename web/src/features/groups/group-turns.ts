import type { GroupTurnEvent, GroupTurnState } from "./use-group-events";

// A terminal turn is history worth showing for a moment -- it is the one thing
// that explains why nobody answered -- then it falls back to idle. "running" is
// not history and never gets a linger timer: it stays until the server replaces
// it with a terminal frame, and a reconnect re-snapshots it, so a lost frame
// self-heals instead of pinning a stale badge forever.
export const GROUP_TURN_LINGER_MS = 6000;

const TERMINAL_TURN_STATES: readonly GroupTurnState[] = ["silent", "held", "failed", "done"];

export function isTerminalTurn(state: GroupTurnState): boolean {
  return TERMINAL_TURN_STATES.includes(state);
}

export function applyTurn(
  turns: Map<string, GroupTurnEvent>,
  turn: GroupTurnEvent,
): Map<string, GroupTurnEvent> {
  return new Map(turns).set(turn.agent_id, turn);
}

// Identity guard: a newer turn for the same agent owns the slot, so an expiring
// timer must not wipe the state that replaced it.
export function expireTurn(
  turns: Map<string, GroupTurnEvent>,
  turn: GroupTurnEvent,
): Map<string, GroupTurnEvent> {
  if (turns.get(turn.agent_id) !== turn) return turns;
  const next = new Map(turns);
  next.delete(turn.agent_id);
  return next;
}

// Belt and braces for a lost "done" frame: an agent's own canonical message
// proves its turn ended, so the message reducer clears the running state even if
// the terminal frame never arrived. A terminal state is left alone -- it is
// already retiring on its linger timer and still explains the turn.
export function clearRunningTurn(
  turns: Map<string, GroupTurnEvent>,
  agentId: string,
): Map<string, GroupTurnEvent> {
  const current = turns.get(agentId);
  if (!current || isTerminalTurn(current.state)) return turns;
  const next = new Map(turns);
  next.delete(agentId);
  return next;
}

// A lingering terminal state is history, not activity: held agents must not
// keep the inspector's "active" badge lit.
export function activeTurnAgentIds(turns: Map<string, GroupTurnEvent>): string[] {
  return [...turns.entries()]
    .filter(([, turn]) => !isTerminalTurn(turn.state))
    .map(([agentId]) => agentId);
}
