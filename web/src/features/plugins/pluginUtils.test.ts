import { describe, expect, it } from "vitest";
import type { ManifestPlugin, PluginWithMeta } from "@/lib/types";
import {
  changedManifestPluginFields,
  pluginFieldIsOverridden,
  pluginIsCustomized,
  pluginIsReleaseManaged,
  pluginIsRemovable,
} from "./pluginUtils";

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

describe("pluginIsCustomized", () => {
  it("marks a builtin whose definition was edited", () => {
    expect(pluginIsCustomized(plugin({ builtin: true, overridden_fields: ["binaries"] }))).toBe(
      true,
    );
  });

  it("leaves an untouched builtin alone", () => {
    expect(pluginIsCustomized(plugin({ builtin: true }))).toBe(false);
    expect(pluginIsCustomized(plugin({ builtin: true, overridden_fields: [] }))).toBe(false);
  });

  it("never marks an admin-added plugin — it has no shipped definition to diverge from", () => {
    expect(pluginIsCustomized(plugin({ builtin: false, overridden_fields: ["binaries"] }))).toBe(
      false,
    );
    expect(pluginIsCustomized(plugin(null))).toBe(false);
  });
});

describe("pluginIsReleaseManaged", () => {
  it("defaults shipped manifest tools to release ownership", () => {
    expect(pluginIsReleaseManaged(plugin({ builtin: true }))).toBe(true);
  });

  it("recognizes tenant-managed and admin-added plugins", () => {
    expect(pluginIsReleaseManaged(plugin({ builtin: true, tenant_managed: true }))).toBe(false);
    expect(pluginIsReleaseManaged(plugin({ builtin: false }))).toBe(false);
    expect(pluginIsReleaseManaged(plugin(null))).toBe(false);
  });
});

describe("pluginFieldIsOverridden", () => {
  it("finds an owned field on a builtin", () => {
    const builtin = plugin({
      builtin: true,
      overridden_fields: ["binaries", "session_env"],
    });
    expect(pluginFieldIsOverridden(builtin, "binaries")).toBe(true);
    expect(pluginFieldIsOverridden(builtin, "oauth_provider")).toBe(false);
  });

  it("never marks fields on admin-added or non-manifest plugins", () => {
    expect(
      pluginFieldIsOverridden(
        plugin({ builtin: false, overridden_fields: ["binaries"] }),
        "binaries",
      ),
    ).toBe(false);
    expect(pluginFieldIsOverridden(plugin(null), "binaries")).toBe(false);
  });
});

describe("changedManifestPluginFields", () => {
  const initial: ManifestPlugin = {
    id: "tool/x",
    kind: "tool",
    name: "x",
    display_name: "X",
    description: "",
    enabled: true,
    binaries: [{ name: "x", tool: "github:example/x", version: "1.0.0" }],
    session_env: [{ env_var: "TOKEN", source: "static", required: true }],
  };

  it("returns only changed top-level definition fields", () => {
    expect(
      changedManifestPluginFields(initial, {
        ...initial,
        enabled: false,
        binaries: [{ name: "x", tool: "github:example/x", version: "2.0.0" }],
        oauth_provider: "github",
      }),
    ).toEqual(["binaries", "oauth_provider"]);
  });

  it("ignores object key order and undefined properties", () => {
    expect(
      changedManifestPluginFields(initial, {
        ...initial,
        binaries: [{ version: "1.0.0", tool: "github:example/x", name: "x", options: undefined }],
        session_env: [{ required: true, source: "static", env_var: "TOKEN" }],
      }),
    ).toEqual([]);
  });

  it("never claims kind or essential, which belong to the server", () => {
    expect(
      changedManifestPluginFields(initial, { ...initial, kind: "hook", essential: true }),
    ).toEqual([]);
  });

  // The editor rebuilds `binaries: []` on every render, and the server drops
  // empty lists and empty strings when it stores a definition. Absent and empty
  // reach it identically, so a save must not claim ownership of the difference.
  it("reads an absent list and an empty one as the same value", () => {
    const bare: ManifestPlugin = { ...initial, binaries: undefined, session_env: undefined };
    expect(changedManifestPluginFields(bare, { ...bare, binaries: [], session_env: [] })).toEqual(
      [],
    );
    expect(changedManifestPluginFields(bare, { ...bare, oauth_provider: "" })).toEqual([]);
  });

  it("still sees emptying a non-empty list, and reordering one", () => {
    expect(changedManifestPluginFields(initial, { ...initial, binaries: [] })).toEqual([
      "binaries",
    ]);
    const two: ManifestPlugin = {
      ...initial,
      binaries: [
        { name: "a", tool: "github:example/a" },
        { name: "b", tool: "github:example/b" },
      ],
    };
    expect(
      changedManifestPluginFields(two, { ...two, binaries: [...two.binaries!].reverse() }),
    ).toEqual(["binaries"]);
  });
});
