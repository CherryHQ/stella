import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import { agentRequestBody, type AgentsPageState } from "../agent-detail-state";
import { AdvancedTab } from "./AdvancedTab";

vi.hoisted(() => {
  Object.defineProperty(globalThis, "localStorage", {
    configurable: true,
    value: { getItem: () => "en", setItem: () => undefined },
  });
});

function state(enabled: boolean): Pick<AgentsPageState, "form"> {
  return {
    form: {
      name: "Agent",
      model: "",
      model_thinking: "",
      model_strong: "",
      model_strong_thinking: "",
      model_fast: "",
      model_fast_thinking: "",
      system_prompt: "",
      soul: "",
      scope: "restricted",
      enabled: true,
      creator_id: "",
      system_settings_tools_enabled: enabled,
      sandbox: { network: { mode: "allow_all", allowlist: [] } },
      template_id: "",
    },
  };
}

describe("AdvancedTab system settings policy", () => {
  it("shows the switch only to an Agent manager", () => {
    const manager = renderToStaticMarkup(
      <AdvancedTab state={state(false)} canEdit onSetState={vi.fn()} />,
    );
    const viewer = renderToStaticMarkup(
      <AdvancedTab state={state(false)} canEdit={false} onSetState={vi.fn()} />,
    );

    expect(manager).toContain("System settings tools");
    expect(manager).toContain('role="switch"');
    expect(viewer).not.toContain("System settings tools");
    expect(viewer).not.toContain('role="switch"');
  });

  it("includes the enabled policy bit in the existing Agent Save body", () => {
    expect(agentRequestBody(state(true).form)).toMatchObject({
      system_settings_tools_enabled: true,
    });
  });
});
