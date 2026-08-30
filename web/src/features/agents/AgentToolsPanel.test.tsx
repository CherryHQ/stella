import type { ComponentProps } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
} from "@tanstack/react-router";
import { describe, expect, it, vi } from "vitest";
import type { Tool } from "@/lib/types";
import {
  groupedRegularTools,
  RegularToolFamilyCard,
  SystemSettingsSection,
  ToolRow,
} from "./AgentToolsPanel";

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

// SAFETY: fixed response fixture mirrors every unavailable email action after
// the server's EMAIL_CONFIG availability check, including its explicit reason.
const unavailableEmailTool = {
  name: "email_account_list",
  description: "List configured email accounts.",
  source: "builtin",
  control: "system",
  family: "email",
  policy_reason: "runtime_unavailable",
  availability_reason: "email_config_required",
} as Tool;

// SAFETY: this derived fixture preserves the unavailable-email API shape.
const unavailableEmailSendTool = {
  ...unavailableEmailTool,
  name: "email_message_send",
  description: "Send email.",
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

async function renderRegularToolFamilyCard(props: ComponentProps<typeof RegularToolFamilyCard>) {
  const rootRoute = createRootRoute();
  const cardRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: "/",
    component: () => <RegularToolFamilyCard {...props} />,
  });
  const router = createRouter({
    routeTree: rootRoute.addChildren([cardRoute]),
    history: createMemoryHistory({ initialEntries: ["/"] }),
  });
  await router.load();
  return renderToStaticMarkup(<RouterProvider router={router} />);
}

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

  it("renders Email as a locked family with its authoritative setup CTA, separate from Goals", async () => {
    const emailHtml = await renderRegularToolFamilyCard({
      family: "email",
      tools: [unavailableEmailTool, unavailableEmailSendTool],
      defaultOpen: false,
      canEdit: true,
      isAdmin: true,
      busyToolName: null,
      onToggle: vi.fn(),
    });
    const goalHtml = await renderRegularToolFamilyCard({
      family: "goal",
      tools: [generatedGoalTool, generatedGoalCreateTool],
      defaultOpen: true,
      canEdit: true,
      isAdmin: true,
      busyToolName: null,
      onToggle: vi.fn(),
    });

    expect(emailHtml).toContain('data-slot="card"');
    expect(emailHtml).toContain('data-slot="collapsible"');
    expect(emailHtml).toMatch(/<h3[^>]*><button/);
    expect(emailHtml).not.toMatch(/<button[^>]*><h3/);
    expect(emailHtml).toContain("Email");
    expect(emailHtml).toContain("2 actions");
    expect(emailHtml).toContain("Email setup required");
    expect(emailHtml).toContain(
      "Configure a personal email account in Credentials to manage this tool.",
    );
    expect(emailHtml).toContain('href="/settings/credentials"');
    expect(emailHtml).not.toContain('role="switch"');
    expect(emailHtml).not.toContain("Runtime availability decides when this tool is registered.");
    expect(goalHtml).toContain("Goals");
    expect(goalHtml).toContain("2 actions");
    expect(goalHtml).toContain("All enabled");
    expect(goalHtml).toContain('role="switch"');
    expect(goalHtml).not.toContain(
      "Configure a personal email account in Credentials to manage this tool.",
    );
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
