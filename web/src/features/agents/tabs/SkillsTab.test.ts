import { describe, expect, it } from "vitest";
import { activationControlState, danglingClearControlState } from "./skills-tab-state";

describe("Agent Skill activation controls", () => {
  it("uses server management authority and keeps pending controls disabled", () => {
    expect(activationControlState("builtin:stella", false, false)).toEqual({
      visible: false,
      disabled: false,
    });
    expect(activationControlState("builtin:stella", true, true)).toEqual({
      visible: true,
      disabled: true,
    });
    expect(activationControlState(undefined, true, false)).toEqual({
      visible: false,
      disabled: false,
    });
  });

  it("keeps each dangling clear action explicit and disabled while its mutation is pending", () => {
    expect(danglingClearControlState(false, false)).toEqual({ visible: false, disabled: false });
    expect(danglingClearControlState(true, false)).toEqual({ visible: true, disabled: false });
    expect(danglingClearControlState(true, true)).toEqual({ visible: true, disabled: true });
  });
});
