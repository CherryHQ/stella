// MCP effective-agent projection and the four-scope tool override surface.
import { createChatSession, ensureAgent, invokedToolNames, sendTurn, sessionMessages } from "./lib/agent.ts";
import { expectStatus } from "./lib/api.ts";
import { expect, test } from "./lib/fixtures.ts";
import { createMcpPlugin, deleteMcpServer, type McpFixture, startMcpFixture } from "./lib/mcp-fixture.ts";
import { ensureProvider } from "./lib/provider.ts";
import type { AgentTool, McpServer } from "./lib/types.ts";

test.describe.configure({ mode: "serial" });
let fixture: McpFixture;
let agentId = "";
let server: McpServer;
let add = "";
let echo = "";

async function tools(
  admin: import("./lib/api.ts").ApiClient,
): Promise<AgentTool[]> {
  return expectStatus(
    await admin.get<{ tools: AgentTool[]; }>(`/api/agents/${agentId}/tools`),
    200,
    "list agent tools",
  ).tools;
}

test.beforeAll(async ({ admin }) => {
  fixture = await startMcpFixture();
  const { modelRef } = await ensureProvider(admin);
  agentId = await ensureAgent(admin, modelRef, "e2e-mcp-permissions");
  server = await createMcpPlugin(admin, fixture, {
    name: "permissions",
    scope: "user_agent",
    agentId,
  });
  server = expectStatus(
    await admin.post<McpServer>(`/api/mcp/servers/${server.id}/probe`),
    200,
    "probe permissions server",
  );
  await expect
    .poll(
      async () => {
        const listed = await tools(admin);
        add = listed.find((item) => item.description?.startsWith("Add two integers"))?.name ?? "";
        echo = listed.find((item) => item.description?.startsWith("Echo the given text"))?.name ?? "";
        return Boolean(add && echo);
      },
      { timeout: 30_000 },
    )
    .toBe(true);
});
test.afterAll(async ({ admin }) => {
  if (server) await deleteMcpServer(admin, server);
  await fixture.close();
});

test("agent endpoint exposes the effective file registration identity", async ({ admin }) => {
  const listed = expectStatus(
    await admin.get<{
      servers: Array<{
        id: string;
        resource_id: string;
        name: string;
        server_key: string;
        content_digest: string;
        status: string;
        readable: boolean;
        tools: { name: string; }[];
      }>;
    }>(`/api/agents/${agentId}/mcp-servers`),
    200,
    "list effective MCP servers",
  );
  const registration = listed.servers.find(
    (item) => item.server_key === "permissions",
  );
  expect(registration).toMatchObject({
    name: "permissions",
    status: "unknown",
    readable: true,
    tools: [],
  });
});

test("tool overrides persist at every scope and precedence is visible", async ({ admin }) => {
  for (
    const [scope, enabled] of [
      ["user", false],
      ["user_agent", true],
      ["system_agent", false],
      ["system", true],
    ] as const
  ) {
    expectStatus(
      await admin.patch<AgentTool>(`/api/agents/${agentId}/tools/${add}`, {
        enabled,
        scope,
      }),
      200,
      `set ${scope} override`,
    );
  }
  expect(findTool(await tools(admin), add)).toMatchObject({
    enabled: false,
    origin: "system_agent",
  });
  expect(
    (
      await admin.patch(
        `/api/agents/${agentId}/tools/permissions_missing_tool`,
        { enabled: false },
      )
    ).status,
  ).toBe(400);
});

function findTool(items: AgentTool[], name: string): AgentTool {
  const found = items.find((item) => item.name === name);
  if (!found) {
    throw new Error(`tool ${name} missing from ${JSON.stringify(items)}`);
  }
  return found;
}

test("profile UI groups MCP tools and persists a browser toggle", async ({ page, admin, loginAsAdmin }) => {
  expectStatus(
    await admin.patch<AgentTool>(`/api/agents/${agentId}/tools/${add}`, {
      enabled: true,
      scope: "user_agent",
    }),
    200,
    "enable add for UI",
  );
  expectStatus(
    await admin.patch<AgentTool>(`/api/agents/${agentId}/tools/${echo}`, {
      enabled: false,
      scope: "user_agent",
    }),
    200,
    "disable echo for UI",
  );
  await loginAsAdmin();
  await page.goto(`/agents/${agentId}/profile?tab=tools`);
  await expect(page.getByText("MCP servers", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "permissions", exact: true }).click();
  await expect(page.getByText(add, { exact: true })).toBeVisible();
  const card = page.locator('[data-slot="card"]').filter({ hasText: echo });
  await card.getByRole("switch").click();
  await expect
    .poll(async () => findTool(await tools(admin), echo).enabled)
    .toBe(true);
});

test("real agent turn only calls the enabled MCP tool @model", async ({ admin }) => {
  test.setTimeout(300_000);
  for (const tool of [add, echo]) {
    for (
      const scope of [
        "user",
        "user_agent",
        "system",
        "system_agent",
      ] as const
    ) {
      await admin.patch(`/api/agents/${agentId}/tools/${tool}`, { scope });
    }
  }
  expectStatus(
    await admin.patch(`/api/agents/${agentId}/tools/${add}`, {
      enabled: true,
      scope: "user_agent",
    }),
    200,
    "enable add",
  );
  expectStatus(
    await admin.patch(`/api/agents/${agentId}/tools/${echo}`, {
      enabled: false,
      scope: "user_agent",
    }),
    200,
    "disable echo",
  );
  await expect
    .poll(async () => findTool(await tools(admin), add).enabled, {
      timeout: 30_000,
    })
    .toBe(true);
  const sessionID = await createChatSession(admin, agentId);
  const before = fixture.calls.length;
  const turn = await sendTurn(
    admin,
    agentId,
    sessionID,
    `Use ${add} with a=17 and b=25. Do not use echo. Reply with only the result.`,
  );
  expect(turn.errors, JSON.stringify(turn.events.slice(-5))).toEqual([]);
  expect(turn.text).toContain("42");
  const calls = fixture.calls.slice(before);
  expect(
    calls.some(
      (call) => call.tool === "add" && call.args.a === 17 && call.args.b === 25,
    ),
  ).toBe(true);
  expect(calls.some((call) => call.tool === "echo")).toBe(false);
  const invoked = invokedToolNames(
    await sessionMessages(admin, agentId, sessionID),
  );
  expect(invoked).toContain(add);
  expect(invoked).not.toContain(echo);
});
