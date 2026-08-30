import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import type { Tool } from "@/lib/types";
import { SystemSettingsSection, ToolRow } from "./AgentToolsPanel";

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
} as Tool;

describe("AgentToolsPanel control contract", () => {
  it("renders Stella Settings as a read-only policy catalog", () => {
    const html = renderToStaticMarkup(<SystemSettingsSection tools={[settingsAction]} />);

    expect(html).toContain("System settings");
    expect(html).toContain("Stella only");
    expect(html).toContain("Foreground 1:1 chat only");
    expect(html).toContain("Agent management");
    expect(html).toContain("agent_update");
    expect(html).not.toContain('role="switch"');
  });

  it("offers a switch only for rows the backend marks override-controlled", () => {
    const systemHtml = renderToStaticMarkup(
      <ToolRow tool={coreTool} canEdit isAdmin busy={false} onToggle={vi.fn()} />,
    );
    const overrideHtml = renderToStaticMarkup(
      <ToolRow tool={overrideTool} canEdit isAdmin busy={false} onToggle={vi.fn()} />,
    );

    expect(systemHtml).toContain("System managed");
    expect(systemHtml).toContain("Core sandbox tools are system-managed.");
    expect(systemHtml).not.toContain('role="switch"');
    expect(overrideHtml).toContain('role="switch"');
    expect(overrideHtml).toContain("Builtin");
    expect(overrideHtml).toContain("Default");
  });
});
