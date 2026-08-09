import { describe, expect, it } from "vitest";
import type { ManifestPlugin, PluginWithMeta } from "@/lib/types";
import { pluginIsRemovable } from "./pluginUtils";

function plugin(manifest: Partial<ManifestPlugin> | null): PluginWithMeta {
  return {
    id: "tool/x",
    kind: "tool",
    name: "x",
    display_name: "X",
    description: "",
    enabled: true,
    config: {},
    capabilities: [],
    has_config: false,
    has_status: false,
    _manifest: !!manifest,
    _manifestPlugin: manifest
      ? ({ id: "tool/x", kind: "tool", name: "x", enabled: true, ...manifest } as ManifestPlugin)
      : null,
  } as PluginWithMeta;
}

describe("pluginIsRemovable", () => {
  it("allows removing a plugin an admin added", () => {
    expect(pluginIsRemovable(plugin({ builtin: false }))).toBe(true);
    expect(pluginIsRemovable(plugin({}))).toBe(true);
  });

  it("refuses a builtin — disabling is its off switch", () => {
    expect(pluginIsRemovable(plugin({ builtin: true }))).toBe(false);
  });

  it("refuses a plugin with no manifest definition to remove", () => {
    expect(pluginIsRemovable(plugin(null))).toBe(false);
  });
});
