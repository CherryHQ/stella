import { describe, expect, it } from "vitest";
import type { GroupTurnEvent } from "./use-group-events";
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
    turns = applyTurn(turns, turn("agent-2", "thinking"));
    expect(activeTurnAgentIds(turns)).toEqual(["agent-2"]);
  });

  it("expires only the turn that scheduled the expiry", () => {
    const held = turn("agent-1", "held", "freshness");
    const turns = applyTurn(new Map(), held);
    const replaced = applyTurn(turns, turn("agent-1", "thinking"));
    expect(expireTurn(replaced, held).get("agent-1")?.state).toBe("thinking");
    expect(expireTurn(turns, held).has("agent-1")).toBe(false);
  });

  it("treats every state the dispatcher emits as terminal", () => {
    expect(["silent", "held", "failed"].every((s) => isTerminalTurn(s as never))).toBe(true);
    expect(isTerminalTurn("thinking")).toBe(false);
  });
});
