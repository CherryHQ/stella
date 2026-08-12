import { describe, expect, it } from "vitest";
import {
  isAgentManagedScope,
  scopeForRange,
  scopeQueriesForBand,
  scopesForBand,
} from "./scope-band";

describe("scope bands", () => {
  it("keeps Personal Settings inside user-owned scopes", () => {
    expect(scopesForBand("personal")).toEqual(["user", "user_agent"]);
    expect(scopeForRange("personal", false)).toBe("user");
    expect(scopeForRange("personal", true)).toBe("user_agent");
  });

  it("keeps Admin Console inside deployment-owned scopes", () => {
    expect(scopesForBand("system")).toEqual(["system", "system_agent"]);
    expect(scopeForRange("system", false)).toBe("system");
    expect(scopeForRange("system", true)).toBe("system_agent");
  });

  it("identifies both agent-specific scopes", () => {
    expect(isAgentManagedScope("user_agent")).toBe(true);
    expect(isAgentManagedScope("system_agent")).toBe(true);
    expect(isAgentManagedScope("user")).toBe(false);
    expect(isAgentManagedScope("system")).toBe(false);
  });

  it("builds disjoint load plans for the two surfaces", () => {
    expect(scopeQueriesForBand("personal", ["a", "b"])).toEqual([
      { scope: "user" },
      { scope: "user_agent", agentID: "a" },
      { scope: "user_agent", agentID: "b" },
    ]);
    expect(scopeQueriesForBand("system", ["a", "b"])).toEqual([
      { scope: "system" },
      { scope: "system_agent", agentID: "a" },
      { scope: "system_agent", agentID: "b" },
    ]);
  });
});
