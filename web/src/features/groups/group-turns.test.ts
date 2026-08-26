import { describe, expect, it } from "vitest";
import type { GroupTurnEvent, GroupTurnState } from "./use-group-events";
import {
  activeTurnAgentIds,
  applyTurn,
  clearRunningTurn,
  expireTurn,
  isTerminalTurn,
} from "./group-turns";

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
    // A lingering terminal turn is history: nothing here may light the
    // inspector's "active" badge.
    expect(activeTurnAgentIds(turns)).toEqual([]);
  });

  it("reports only running agents as active", () => {
    let turns = applyTurn(new Map(), turn("agent-1", "running", "mention"));
    turns = applyTurn(turns, turn("agent-2", "silent", "hard_cap"));
    expect(activeTurnAgentIds(turns)).toEqual(["agent-1"]);
  });

  it("retires a running turn when its done frame arrives", () => {
    let turns = applyTurn(new Map(), turn("agent-1", "running", "mention"));
    turns = applyTurn(turns, turn("agent-1", "done"));
    expect(turns.get("agent-1")?.state).toBe("done");
    expect(activeTurnAgentIds(turns)).toEqual([]);
  });

  it("clears a running turn on the agent's own message, and leaves terminal ones", () => {
    const running = applyTurn(new Map(), turn("agent-1", "running", "mention"));
    expect(clearRunningTurn(running, "agent-1").has("agent-1")).toBe(false);

    const held = applyTurn(new Map(), turn("agent-1", "held", "freshness"));
    expect(clearRunningTurn(held, "agent-1")).toBe(held);
    expect(clearRunningTurn(held, "agent-2")).toBe(held);
  });

  it("expires only the turn that scheduled the expiry", () => {
    const held = turn("agent-1", "held", "freshness");
    const turns = applyTurn(new Map(), held);
    const replaced = applyTurn(turns, turn("agent-1", "silent"));
    expect(expireTurn(replaced, held).get("agent-1")?.state).toBe("silent");
    expect(expireTurn(turns, held).has("agent-1")).toBe(false);
  });

  // The stop button aborts exactly activeTurnAgentIds(turns) and hides when that
  // list is empty; this walks the lifecycle it reads.
  it("narrows the stop button's targets as turns end", () => {
    let turns = applyTurn(new Map(), turn("agent-1", "running", "mention"));
    turns = applyTurn(turns, turn("agent-2", "running", "wake"));
    expect(activeTurnAgentIds(turns).sort()).toEqual(["agent-1", "agent-2"]);

    // agent-1's canonical message lands without its done frame.
    turns = clearRunningTurn(turns, "agent-1");
    expect(activeTurnAgentIds(turns)).toEqual(["agent-2"]);

    turns = applyTurn(turns, turn("agent-2", "done"));
    expect(activeTurnAgentIds(turns)).toEqual([]);
  });

  it("treats every end state as terminal and running as live", () => {
    const endStates: GroupTurnState[] = ["silent", "held", "failed", "done"];
    expect(endStates.every(isTerminalTurn)).toBe(true);
    // "running" must never linger-expire: it is retired by a terminal frame, or
    // re-derived from the reconnect snapshot. Same for any future live state.
    expect(isTerminalTurn("running")).toBe(false);
    // SAFETY: "thinking" is a live state by the same invariant as "running" in this contract test.
    expect(isTerminalTurn("thinking" as GroupTurnState)).toBe(false);
  });
});
