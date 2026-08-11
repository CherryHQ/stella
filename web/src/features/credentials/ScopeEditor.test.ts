import { describe, expect, it } from "vitest";
import { buildOAuthAllowedScopeOverride } from "./scope-policy";

describe("buildOAuthAllowedScopeOverride", () => {
  const manifestAllowed = ["profile", "documents.read", "documents.write"];
  const defaults = ["profile"];

  it("uses an empty override only for the unchanged manifest policy", () => {
    expect(buildOAuthAllowedScopeOverride(manifestAllowed, manifestAllowed, defaults)).toEqual([]);
  });

  it("persists the default floor when the allowlist is narrowed to nothing", () => {
    expect(buildOAuthAllowedScopeOverride([], manifestAllowed, defaults)).toEqual(["profile"]);
  });

  it("unions the default floor into a custom allowlist", () => {
    expect(buildOAuthAllowedScopeOverride(["documents.read"], manifestAllowed, defaults)).toEqual([
      "profile",
      "documents.read",
    ]);
  });

  it("uses the edited default floor when both scope sets are saved together", () => {
    expect(buildOAuthAllowedScopeOverride([], manifestAllowed, ["identity"])).toEqual(["identity"]);
  });
});
