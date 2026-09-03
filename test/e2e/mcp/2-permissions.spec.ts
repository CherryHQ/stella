// PR #1234: MCP catalog tools use the four-scope tool_override model in the
// API, profile UI, persisted rows, and the real agent runner.
import { expectStatus } from "../lib/api.ts";
import { createChatSession, ensureAgent, invokedToolNames, sendTurn, sessionMessages } from "../lib/agent.ts";
import { expect, test } from "../lib/fixtures.ts";
import { startMcpFixture, type McpFixture } from "../lib/mcp-fixture.ts";
import { ensureProvider } from "../lib/provider.ts";

interface AgentTool {
  name: string;
  source: string;
  control: string;
  enabled?: boolean;
  origin?: string;
  family?: string;
  availability_reason?: string;
}

interface McpServer {
  id: string;
  name: string;
  scope: string;
  status: string;
  enabled: boolean;
  tools?: { name: string }[];
}

interface AgentMcpServer extends McpServer {
  readable: boolean;
  shadowed_scopes?: string[];
}

test.describe.configure({ mode: "serial" });

let fixture: McpFixture;
let serverId = "";
let agentId = "";
let sessionId = "";

async function agentTools(admin: import("../lib/api.ts").ApiClient): Promise<AgentTool[]> {
  return expectStatus(
    await admin.get<{ tools: AgentTool[] }>(`/api/agents/${agentId}/tools`),
    200,
    "list agent tools",
  ).tools;
}

function findTool(tools: AgentTool[], name: string): AgentTool {
  const tool = tools.find((item) => item.name === name);
  if (!tool) throw new Error(`tool ${name} missing from ${JSON.stringify(tools)}`);
  return tool;
}

test.beforeAll(async () => {
  fixture = await startMcpFixture();
});

test.afterAll(async ({ admin }) => {
  if (serverId) await admin.delete(`/api/mcp/servers/${serverId}`);
  await fixture.close();
});

test("catalog endpoint exposes effective MCP registration and tools", async ({ admin, db }) => {
  const { modelRef } = await ensureProvider(admin);
  agentId = await ensureAgent(admin, modelRef, "e2e-mcp-permissions");

  const created = expectStatus(
    await admin.post<McpServer>("/api/mcp/servers", {
      scope: "user",
      name: "permissions",
      url: fixture.url,
      transport: "streamable_http",
      auth_type: "none",
    }),
    201,
    "create permissions server",
  );
  serverId = created.id;
  expect(created.status).toBe("ok");
  expect(created.tools?.map((tool) => tool.name).sort()).toEqual(["add", "echo"]);

  const servers = expectStatus(
    await admin.get<{ servers: AgentMcpServer[] }>(`/api/agents/${agentId}/mcp-servers`),
    200,
    "list agent MCP servers",
  );
  const registration = servers.servers.find((server) => server.id === serverId);
  expect(registration).toMatchObject({ name: "permissions", scope: "user", readable: true });
  expect(registration?.shadowed_scopes ?? []).toEqual([]);

  const tools = await agentTools(admin);
  for (const name of ["mcp__permissions__add", "mcp__permissions__echo"]) {
    expect(findTool(tools, name)).toMatchObject({
      source: "mcp",
      control: "override",
      enabled: true,
      origin: "default",
      family: "mcp:permissions",
    });
  }

  const rows = await db`
    select name, scope, enabled, status, tools
    from mcp_server where id = ${serverId}`;
  expect(rows).toHaveLength(1);
  expect(rows[0]).toMatchObject({ name: "permissions", scope: "user", enabled: true, status: "ok" });
  expect((rows[0].tools as { name: string }[]).map((tool) => tool.name).sort()).toEqual(["add", "echo"]);
});

test("PATCH writes all four scopes and admin disable wins", async ({ admin, db }) => {
  const add = "mcp__permissions__add";

  for (const [scope, enabled, origin] of [
    ["user", false, "user"],
    ["user_agent", true, "user_agent"],
    ["system_agent", false, "system_agent"],
    ["system", true, "system_agent"],
  ] as const) {
    const body = expectStatus(
      await admin.patch<AgentTool>(`/api/agents/${agentId}/tools/${add}`, { enabled, scope }),
      200,
      `set ${scope} override`,
    );
    expect(body.name).toBe(add);
  }

  const rows = await db`
    select tool_name, scope, user_id, agent_id, enabled
    from tool_override
    where tool_name = ${add}
    order by scope`;
  expect(rows).toHaveLength(4);
  expect(rows.map((row) => [row.scope, row.enabled])).toEqual([
    ["system", true],
    ["system_agent", false],
    ["user", false],
    ["user_agent", true],
  ]);
  expect(rows.find((row) => row.scope === "system")?.user_id).toBeNull();
  expect(rows.find((row) => row.scope === "system_agent")?.agent_id).toBe(agentId);

  expect(findTool(await agentTools(admin), add)).toMatchObject({ enabled: false, origin: "system_agent" });

  const unknown = await admin.patch(`/api/agents/${agentId}/tools/mcp__permissions__missing`, { enabled: false });
  expect(unknown.status).toBe(400);
});

test("profile UI groups MCP tools and persists a browser toggle", async ({ page, admin, db, loginAsAdmin }) => {
  // Leave add enabled for the agent turn and disable echo through the same API
  // surface the browser uses, so the UI has both effective states to render.
  expectStatus(
    await admin.patch<AgentTool>(`/api/agents/${agentId}/tools/mcp__permissions__add`, {
      enabled: true,
      scope: "user_agent",
    }),
    200,
    "enable add for UI",
  );
  expectStatus(
    await admin.patch<AgentTool>(`/api/agents/${agentId}/tools/mcp__permissions__echo`, {
      enabled: false,
      scope: "user_agent",
    }),
    200,
    "disable echo for UI",
  );

  await loginAsAdmin();
  await page.goto(`/agents/${agentId}/profile?tab=tools`);
  await expect(page.getByText("MCP servers", { exact: true })).toBeVisible();
  await expect(page.getByText("permissions", { exact: true })).toBeVisible();
  await page.getByText("permissions", { exact: true }).click();
  await expect(page.getByText("mcp__permissions__add", { exact: true })).toBeVisible();
  await expect(page.getByText("mcp__permissions__echo", { exact: true })).toBeVisible();
  await expect(page.getByText("Disabled", { exact: true }).first()).toBeVisible();

  const echoCard = page.locator('[data-slot="card"]').filter({ hasText: "mcp__permissions__echo" });
  await echoCard.getByRole("switch").click();
  await expect.poll(async () => {
    const row = await db`
      select enabled from tool_override
      where tool_name = 'mcp__permissions__echo' and scope = 'user_agent'
        and agent_id = ${agentId}`;
    return row[0]?.enabled;
  }).toBe(true);
});

test("real agent turn only calls the enabled MCP tool", async ({ admin }) => {
  test.setTimeout(300_000);
  const add = "mcp__permissions__add";
  const echo = "mcp__permissions__echo";
  // Remove the admin rows created by the previous four-scope assertion before
  // proving that a user-agent enable reaches the runner.
  for (const scope of ["system_agent", "system", "user", "user_agent"] as const) {
    expectStatus(
      await admin.patch<AgentTool>(`/api/agents/${agentId}/tools/${add}`, { scope }),
      200,
      `clear ${scope} override`,
    );
  }
  expectStatus(
    await admin.patch<AgentTool>(`/api/agents/${agentId}/tools/${add}`, { enabled: true, scope: "user_agent" }),
    200,
    "enable add for runner",
  );
  expectStatus(
    await admin.patch<AgentTool>(`/api/agents/${agentId}/tools/${echo}`, { enabled: false, scope: "user_agent" }),
    200,
    "disable echo for runner",
  );
  sessionId = await createChatSession(admin, agentId);
  const callsBefore = fixture.calls.length;
  const turn = await sendTurn(
    admin,
    agentId,
    sessionId,
    "Use mcp__permissions__add with a=17 and b=25. Do not use echo. Reply with only the result.",
  );
  expect(turn.errors, JSON.stringify(turn.events.slice(-5))).toEqual([]);
  expect(turn.text).toContain("42");
  // Code Mode may wrap the remote call in an outer `code` tool event; the
  // fixture call and persisted child-call audit are the authoritative proof.
  expect(turn.toolCalls.map((call) => call.toolName)).not.toContain(echo);
  const calls = fixture.calls.slice(callsBefore);
  expect(calls.some((call) => call.tool === "add" && call.args.a === 17 && call.args.b === 25)).toBe(true);
  expect(calls.some((call) => call.tool === "echo")).toBe(false);

  const messages = await sessionMessages(admin, agentId, sessionId);
  const invoked = invokedToolNames(messages);
  expect(invoked).toContain(add);
  expect(invoked).not.toContain(echo);
});
