// MCP Registry search, detail, standalone install, and agent use.
import { createChatSession, ensureAgent, invokedToolNames, sendTurn, sessionMessages } from "./lib/agent.ts";
import { expectStatus } from "./lib/api.ts";
import { expect, test } from "./lib/fixtures.ts";
import { createMcpPlugin, deleteMcpServer, type McpFixture, startMcpFixture } from "./lib/mcp-fixture.ts";
import { ensureProvider } from "./lib/provider.ts";
import { loadRegistryFixtureState } from "./lib/registry-fixture.ts";
import type { AgentTool, McpServer, RegistryServer } from "./lib/types.ts";

test.describe.configure({ mode: "serial" });
const state = loadRegistryFixtureState();
let fixture: McpFixture;
const created: McpServer[] = [];

test.beforeAll(async () => {
  fixture = await startMcpFixture();
});
test.afterAll(async ({ admin }) => {
  for (const server of created) await deleteMcpServer(admin, server);
  await fixture.close();
});

test("registry API filters transports and resumes at an upstream page boundary", async ({ admin }) => {
  const first = expectStatus(
    await admin.get<{ servers: RegistryServer[]; next_page_token?: string; }>(
      "/api/mcp/registry/servers?q=anything&page_size=2",
    ),
    200,
    "registry search",
  );
  expect(first.servers.map((item) => item.auth)).toEqual(["none", "bearer"]);
  expect(
    first.servers.every((item) => item.transport === "streamable_http"),
  ).toBe(true);
  expect(first.next_page_token).toBeTruthy();
  const second = expectStatus(
    await admin.get<{ servers: RegistryServer[]; next_page_token?: string; }>(
      `/api/mcp/registry/servers?page_size=2&page_token=${encodeURIComponent(first.next_page_token!)}`,
    ),
    200,
    "registry next page",
  );
  expect(second.servers.map((item) => item.auth)).toEqual(["unsupported"]);
  expect(second.next_page_token).toBeFalsy();
});

test("registry detail and upstream failures map cleanly", async ({ admin }) => {
  const detail = expectStatus(
    await admin.get<RegistryServer>(
      `/api/mcp/registry/servers/official/${encodeURIComponent("com.stella/registry-add")}`,
    ),
    200,
    "registry detail",
  );
  expect(detail.version).toBe("1.0.0");
  expect(detail.url).toBe(state.mcpUrl);
  expect(
    (
      await admin.get(
        `/api/mcp/registry/servers/official/${encodeURIComponent("com.stella/unknown")}`,
      )
    ).status,
  ).toBe(404);
  expect(
    (await admin.get("/api/mcp/registry/servers?q=rate-limit")).status,
  ).toBe(503);
  expect(
    (await admin.get("/api/mcp/registry/servers?q=upstream-error")).status,
  ).toBe(502);
});

test("registry install creates a probed file resource and rejects duplicate names", async ({ admin }) => {
  const installed = await createMcpPlugin(
    admin,
    { url: state.mcpUrl },
    { name: "registry-add" },
  );
  created.push(installed);
  const probed = expectStatus(
    await admin.post<McpServer>(`/api/mcp/servers/${installed.id}/probe`),
    200,
    "probe registry server",
  );
  expect(probed.status).toBe("ready");
  expect(probed.tools.map((tool) => tool.name).sort()).toEqual(["add", "echo"]);
  const twin = await admin.post("/api/mcp/servers", {
    name: "registry-add",
    scope: "user",
    declaration: {
      url: state.mcpUrl,
      transport: "streamable_http",
      auth_type: "none",
      credential_mode: "shared",
    },
  });
  expect(twin.status).toBe(409);
});

test("a real agent calls add on the registry-installed server @model", async ({ admin }) => {
  test.setTimeout(300_000);
  const installed = created[0]
    ?? (await createMcpPlugin(
      admin,
      { url: state.mcpUrl },
      { name: "registry-add" },
    ));
  if (!created.includes(installed)) created.push(installed);
  const { modelRef } = await ensureProvider(admin);
  const agentId = await ensureAgent(admin, modelRef, "e2e-registry-agent");
  await expect
    .poll(
      async () => {
        const tools = expectStatus(
          await admin.get<{ tools: AgentTool[]; }>(
            `/api/agents/${agentId}/tools`,
          ),
          200,
          "list registry tools",
        ).tools;
        return tools.some((item) => item.name.includes("_main_add_"));
      },
      { timeout: 30_000 },
    )
    .toBe(true);
  const registryAdd = expectStatus(
    await admin.get<{ tools: AgentTool[]; }>(`/api/agents/${agentId}/tools`),
    200,
    "list registry tools",
  ).tools.find((item) => item.name.includes("_main_add_"))?.name;
  if (!registryAdd) throw new Error("registry add tool missing");
  const sessionID = await createChatSession(admin, agentId);
  const turn = await sendTurn(
    admin,
    agentId,
    sessionID,
    `Call ${registryAdd} with a=17 and b=25. Reply with only the result.`,
  );
  expect(turn.errors, JSON.stringify(turn.events.slice(-5))).toEqual([]);
  expect(turn.text).toContain("42");
  expect(
    invokedToolNames(await sessionMessages(admin, agentId, sessionID)),
  ).toContain(registryAdd);
});

test("live registry search has the expected response shape", async () => {
  test.skip(
    !process.env.STELLA_E2E_LIVE_REGISTRY,
    "set STELLA_E2E_LIVE_REGISTRY=1 to run the external smoke check",
  );
  const response = await fetch(
    "https://registry.modelcontextprotocol.io/v0/servers?search=notion&limit=5",
  );
  expect(response.ok).toBe(true);
  const body = (await response.json()) as {
    servers?: unknown[];
    metadata?: { nextCursor?: unknown; };
  };
  expect(Array.isArray(body.servers)).toBe(true);
});
