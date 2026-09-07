import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import type { PluginConfig, PluginDefinition } from "@/lib/api-client";
import { PluginConfigEditor } from "./PluginConfigEditor";

vi.hoisted(() => {
  Object.defineProperty(globalThis, "localStorage", {
    configurable: true,
    value: { getItem: () => "en", setItem: () => undefined },
  });
});

const plugin = (backend: "cli" | "mcp"): PluginDefinition =>
  ({
    display_name: backend === "cli" ? "Lark CLI" : "Remote MCP",
    resource_summary: {
      binaries: backend === "cli" ? [{ name: "lark", version: "1.2.3" }] : [],
      skills: [],
      session_env: [],
      oauth_provider_configured: false,
      mcp_servers:
        backend === "mcp"
          ? [
              {
                server_key: "main",
                endpoint_configured: true,
                bearer_configured: true,
                oauth_client_id_configured: false,
                oauth_client_secret_configured: false,
              },
            ]
          : [],
    },
  }) as unknown as PluginDefinition;

const config = (resource_summary: PluginConfig["resource_summary"]): PluginConfig =>
  ({
    id: "0198f9a4-1b2c-7def-8123-456789abcdef",
    plugin_id: "custom/0198f9a4-1b2c-7def-8123-456789abcdef",
    scope: "user",
    is_enabled: false,
    resource_summary,
    revision: 2,
    created_at: "2026-09-06T00:00:00Z",
    updated_at: "2026-09-06T00:00:00Z",
  }) as PluginConfig;

describe("PluginConfigEditor", () => {
  it("shows independently editable CLI versions without skill mutation fields", () => {
    const html = renderToStaticMarkup(
      <PluginConfigEditor
        plugin={plugin("cli")}
        config={config({
          binaries: [{ name: "lark", version: "1.2.3" }],
          skills: [{ name: "calendar" }],
          session_env: [],
          oauth_provider_configured: false,
          mcp_servers: [],
        })}
        onSave={() => undefined}
        onCancel={() => undefined}
        busy={false}
      />,
    );

    expect(html).toContain("Binary versions");
    expect(html).not.toContain("Skill sources");
    expect(html).toContain("lark");
    expect(html).not.toContain("calendar");
  });

  it("does not echo an existing MCP endpoint while preparing its edit form", () => {
    const html = renderToStaticMarkup(
      <PluginConfigEditor
        plugin={plugin("cli")}
        config={config({
          binaries: [],
          skills: [],
          session_env: [],
          oauth_provider_configured: false,
          mcp_servers: [
            {
              server_key: "main",
              transport: "streamable_http",
              auth_type: "bearer",
              credential_mode: "shared",
              endpoint_configured: true,
              bearer_configured: true,
              oauth_client_id_configured: false,
              oauth_client_secret_configured: false,
            },
          ],
        })}
        mcpServerKey="main"
        onSave={() => undefined}
        onCancel={() => undefined}
        busy={false}
      />,
    );

    expect(html).toContain("Blank fields keep the current value");
    expect(html).not.toContain("https://secret.example");
    expect(html).not.toContain("bearer-token");
  });

  it("uses the selected config child when the definition has no MCP summary", () => {
    const html = renderToStaticMarkup(
      <PluginConfigEditor
        plugin={plugin("cli")}
        config={config({
          binaries: [],
          skills: [],
          session_env: [],
          oauth_provider_configured: false,
          mcp_servers: [
            {
              server_key: "child",
              transport: "sse",
              auth_type: "oauth",
              credential_mode: "shared",
              endpoint_configured: true,
              bearer_configured: false,
              oauth_client_id_configured: true,
              oauth_client_secret_configured: true,
            },
          ],
        })}
        mcpServerKey="child"
        onSave={() => undefined}
        onCancel={() => undefined}
        busy={false}
      />,
    );

    expect(html).toContain("Blank fields keep the current value");
    expect(html).toContain("Authentication");
    expect(html).not.toContain("Binary versions");
  });
});
