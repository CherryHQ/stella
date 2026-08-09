import { describe, expect, it } from "vitest";
import type { Agent } from "@/lib/types";
import { bindableAgents } from "./channel-access";

const agents = [
  { id: "mine", name: "Mine", creator_id: "u1" },
  { id: "theirs", name: "Theirs", creator_id: "u2" },
  { id: "orphan", name: "Orphan", creator_id: "" },
] as Agent[];

describe("bindableAgents", () => {
  it("offers every agent to an admin", () => {
    expect(bindableAgents(agents, { id: "admin", is_admin: true }).map((a) => a.id)).toEqual([
      "mine",
      "theirs",
      "orphan",
    ]);
  });

  it("offers an ordinary user only the agents they created", () => {
    expect(bindableAgents(agents, { id: "u1", is_admin: false }).map((a) => a.id)).toEqual([
      "mine",
    ]);
  });

  it("offers nothing before the viewer is known", () => {
    expect(bindableAgents(agents, undefined)).toEqual([]);
  });
});
