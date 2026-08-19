import type { GroupTurnEvent, GroupTurnState } from "./use-group-events";

// Every turn state the server emits today is terminal: an agent either stayed
// quiet (silent/held) or failed. Dropping those on arrival left the inspector
// permanently blank, which hid the one thing worth showing in a group -- why
// nobody answered. A finished turn stays visible briefly, then falls back to
// idle.
export const GROUP_TURN_LINGER_MS = 6000;

const TERMINAL_TURN_STATES: readonly GroupTurnState[] = ["done", "silent", "held", "failed"];

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

// A lingering terminal state is history, not activity: held agents must not
// keep the inspector's "active" badge lit.
export function activeTurnAgentIds(turns: Map<string, GroupTurnEvent>): string[] {
  return [...turns.entries()]
    .filter(([, turn]) => !isTerminalTurn(turn.state))
    .map(([agentId]) => agentId);
}
