import { describe, expect, it } from "vitest";
import type { GroupTurnEvent, GroupTurnState } from "./use-group-events";
import { activeTurnAgentIds, applyTurn, expireTurn, isTerminalTurn } from "./group-turns";

const turn = (
  agent_id: string,
  state: GroupTurnEvent["state"],
  reason?: string,
): GroupTurnEvent => ({
  agent_id,
  state,
  reason,
});

describe("group turn state", () => {
  it("keeps a terminal turn visible instead of dropping it on arrival", () => {
    const held = turn("agent-1", "held", "freshness");
    const turns = applyTurn(new Map(), held);
    expect(turns.get("agent-1")).toEqual(held);
  });

  it("does not count a finished turn as activity", () => {
    let turns = applyTurn(new Map(), turn("agent-1", "held", "freshness"));
    turns = applyTurn(turns, turn("agent-2", "silent", "nothing to add"));
    // Every state the server emits today is terminal, so a lingering turn is
    // history: nothing here may light the inspector's "active" badge.
    expect(activeTurnAgentIds(turns)).toEqual([]);
  });

  it("expires only the turn that scheduled the expiry", () => {
    const held = turn("agent-1", "held", "freshness");
    const turns = applyTurn(new Map(), held);
    const replaced = applyTurn(turns, turn("agent-1", "silent"));
    expect(expireTurn(replaced, held).get("agent-1")?.state).toBe("silent");
    expect(expireTurn(turns, held).has("agent-1")).toBe(false);
  });

  it("treats every state the dispatcher emits as terminal", () => {
    const everyState: GroupTurnState[] = ["silent", "held", "failed"];
    expect(everyState.every(isTerminalTurn)).toBe(true);
    // A state outside the union can only come from a server that grew a live
    // progress frame, and that must read as activity, not as history.
    expect(isTerminalTurn("thinking" as GroupTurnState)).toBe(false);
  });
});
