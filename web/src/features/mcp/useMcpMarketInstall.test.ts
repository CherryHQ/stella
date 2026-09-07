import { describe, expect, it } from "vitest";
import { registryPluginID } from "./useMcpMarketInstall";

describe("registryPluginID", () => {
  it("normalizes registry path punctuation without changing collision behavior", () => {
    expect(registryPluginID("com.stella/registry-add")).toBe("com-stella-registry-add");
    expect(registryPluginID("vendor..server/")).toBe("vendor-server");
  });

  it("rejects an id with no plugin-id-safe content", () => {
    expect(() => registryPluginID("///")).toThrow(
      "registry server id cannot produce a valid plugin ID",
    );
  });
});
