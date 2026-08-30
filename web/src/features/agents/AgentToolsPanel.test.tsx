import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import type { Tool } from "@/lib/types";
import { groupedRegularTools, SystemSettingsSection, ToolRow } from "./AgentToolsPanel";

vi.hoisted(() => {
  Object.defineProperty(globalThis, "localStorage", {
    configurable: true,
    value: { getItem: () => "en", setItem: () => undefined },
  });
});

// SAFETY: fixed response fixture satisfies the policy catalog shape.
const settingsAction = {
  name: "agent_update",
  description: "Update one agent.",
  source: "builtin",
  control: "system",
  family: "agent_management",
  policy_reason: "settings_policy",
} as Tool & { family: "agent_management" };

// SAFETY: fixed response fixture satisfies the system core row shape.
const coreTool = {
  name: "bash",
  description: "Run a command.",
  source: "core",
  control: "system",
  policy_reason: "core_sandbox",
} as Tool;

// SAFETY: fixed response fixture satisfies the override-controlled row shape.
const overrideTool = {
  name: "vault_secret_list",
  description: "List secret names.",
  source: "builtin",
  control: "override",
  enabled: true,
  origin: "default",
  family: "vault",
} as Tool;

// SAFETY: fixed response fixture satisfies the runtime-unavailable catalog shape.
const runtimeUnavailableTool = {
  name: "recally_article_list",
  description: "List saved articles.",
  source: "builtin",
  control: "system",
  family: "recally",
  policy_reason: "runtime_unavailable",
} as Tool;

// SAFETY: fixed response fixture satisfies a generated builtin catalog row.
const generatedGoalTool = {
  name: "goal_list",
  description: "List goals.",
  source: "builtin",
  control: "override",
  enabled: true,
  origin: "default",
  family: "goal",
} as Tool;

// SAFETY: derived fixture preserves the generated builtin catalog shape.
const generatedGoalCreateTool = {
  ...generatedGoalTool,
  name: "goal_create",
  description: "Create a goal.",
} as Tool;

// SAFETY: fixed response fixture satisfies the plugin fallback catalog shape.
const generatedLookingPlugin = {
  name: "goal_helper",
  description: "Plugin helper.",
  source: "plugin",
  control: "override",
  enabled: true,
  origin: "default",
  family: "plugin_tools",
} as Tool;

describe("AgentToolsPanel control contract", () => {
  it("renders Stella Settings as a read-only policy catalog", () => {
    const html = renderToStaticMarkup(<SystemSettingsSection tools={[settingsAction]} />);

    expect(html).toContain("System settings");
    expect(html).toContain("Stella only");
    expect(html).toContain("Foreground 1:1 chat only");
    expect(html).toContain("Agent management");
    expect(html).toContain("agent_update");
    expect(html).toMatch(/<h3[^>]*><button/);
    expect(html).not.toMatch(/<button[^>]*><h3/);
    expect(html).not.toContain('role="switch"');
  });

  it("groups regular rows by backend family without treating source or a name prefix as navigation", () => {
    const groups = groupedRegularTools([
      generatedGoalTool,
      generatedLookingPlugin,
      runtimeUnavailableTool,
      generatedGoalCreateTool,
    ]);

    expect(groups.map((group) => [group.family, group.tools.map((tool) => tool.name)])).toEqual([
      ["goal", ["goal_create", "goal_list"]],
      ["recally", ["recally_article_list"]],
      ["plugin_tools", ["goal_helper"]],
    ]);
  });

  it("offers a switch only for rows the backend marks override-controlled", () => {
    const systemHtml = renderToStaticMarkup(
      <ToolRow tool={coreTool} canEdit isAdmin busy={false} onToggle={vi.fn()} />,
    );
    const runtimeUnavailableHtml = renderToStaticMarkup(
      <ToolRow tool={runtimeUnavailableTool} canEdit isAdmin busy={false} onToggle={vi.fn()} />,
    );
    const overrideHtml = renderToStaticMarkup(
      <ToolRow tool={overrideTool} canEdit isAdmin busy={false} onToggle={vi.fn()} />,
    );

    expect(systemHtml).toContain("System managed");
    expect(systemHtml).toContain("Core sandbox tools are system-managed.");
    expect(systemHtml).not.toContain('role="switch"');
    expect(runtimeUnavailableHtml).toContain(
      "Runtime availability decides when this tool is registered.",
    );
    expect(runtimeUnavailableHtml).not.toContain('role="switch"');
    expect(overrideHtml).toContain('role="switch"');
    expect(overrideHtml).toContain("Builtin");
    expect(overrideHtml).toContain("Default");
  });
});
