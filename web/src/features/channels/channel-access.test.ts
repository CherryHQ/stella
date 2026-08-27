import { describe, expect, it } from "vitest";
import type { Agent } from "@/lib/types";
import { bindableAgents } from "./channel-access";

// SAFETY: fixed test fixtures shaped as Agent records.
const agents = [
  { id: "mine", name: "Mine", can_manage: true },
  { id: "theirs", name: "Theirs", can_manage: false },
  { id: "unknown", name: "Unknown" },
] as Agent[];

describe("bindableAgents", () => {
  it("offers only the agents the server says the caller may manage", () => {
    expect(bindableAgents(agents).map((a) => a.id)).toEqual(["mine"]);
  });

  it("offers nothing when the server said nothing", () => {
    // SAFETY: the fixture record satisfies the subset of Agent this test needs.
    expect(bindableAgents([{ id: "a", name: "A" } as Agent])).toEqual([]);
    expect(bindableAgents([])).toEqual([]);
  });
});
