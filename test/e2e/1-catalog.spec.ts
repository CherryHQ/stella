// MCP file resources: declaration, probe, credentials, listing, and CAS.
import { createChatSession, ensureAgent, sendTurn } from "./lib/agent.ts";
import { expectStatus } from "./lib/api.ts";
import { expect, test } from "./lib/fixtures.ts";
import { createMcpPlugin, deleteMcpServer, type McpFixture, startMcpFixture } from "./lib/mcp-fixture.ts";
import { ensureProvider } from "./lib/provider.ts";
import type { AgentTool, McpServer } from "./lib/types.ts";

test.describe.configure({ mode: "serial" });
let open: McpFixture;
let guarded: McpFixture;
const created: McpServer[] = [];

test.beforeAll(async () => {
  open = await startMcpFixture();
  guarded = await startMcpFixture({ bearer: "s3cret-token" });
});
test.afterAll(async ({ admin }) => {
  for (const server of created) await deleteMcpServer(admin, server);
  await open.close();
  await guarded.close();
});

test("create stores a file resource and probe publishes its catalog", async ({ admin }) => {
  const server = await createMcpPlugin(admin, open, { name: "e2e" });
  created.push(server);
  expect(server.resource_id).toBeTruthy();
  expect(server.is_standalone).toBe(true);
  expect(server.declaration).toMatchObject({
    url: open.url,
    auth_type: "none",
  });
  const probed = expectStatus(
    await admin.post<McpServer>(`/api/mcp/servers/${server.id}/probe`),
    200,
    "probe created MCP server",
  );
  expect(probed.status).toBe("ready");
  expect(probed.tools.map((tool) => tool.name).sort()).toEqual(["add", "echo"]);
  expect(open.methods.get("initialize")).toBeGreaterThanOrEqual(1);
  expect(open.methods.get("tools/list")).toBeGreaterThanOrEqual(1);
  const listed = expectStatus(
    await admin.get<{ servers: McpServer[]; }>("/api/mcp/servers?scope=user"),
    200,
    "list MCP servers",
  );
  expect(listed.servers.some((item) => item.id === server.id)).toBe(true);
});

test("unreachable endpoint records an error after an explicit probe", async ({ admin }) => {
  const server = await createMcpPlugin(admin, open, {
    name: "e2e-dead",
    url: "http://127.0.0.1:9/mcp",
  });
  created.push(server);
  const probed = expectStatus(
    await admin.post<McpServer>(`/api/mcp/servers/${server.id}/probe`),
    200,
    "probe dead MCP server",
  );
  expect(probed.status).toBe("error");
  expect(probed.status_error).toBeTruthy();
  expect(probed.status_error).not.toMatch(
    /127\.0\.0\.1|connection refused|dial tcp/,
  );
  expect(probed.tools).toEqual([]);
});

test("bearer credentials are redacted and CAS-protected", async ({ admin }) => {
  const server = await createMcpPlugin(admin, guarded, {
    name: "e2e-guarded",
    authType: "bearer",
    credentialMode: "per_user",
    credentialRef: "E2E_BEARER_TOKEN",
    token: "wrong-token",
  });
  created.push(server);
  expect(JSON.stringify(server)).not.toContain("wrong-token");
  const needsAuth = expectStatus(
    await admin.post<McpServer>(`/api/mcp/servers/${server.id}/probe`),
    200,
    "probe guarded MCP server",
  );
  expect(needsAuth.status).toBe("needs_auth");
  const fixed = expectStatus(
    await admin.patch<McpServer>(`/api/mcp/servers/${server.id}/credentials`, {
      expected_digest: needsAuth.content_digest,
      bearer_token: "s3cret-token",
    }),
    200,
    "replace bearer credential",
  );
  const ready = expectStatus(
    await admin.post<McpServer>(`/api/mcp/servers/${server.id}/probe`),
    200,
    "probe fixed token",
  );
  expect(ready.status).toBe("ready");
  expect(JSON.stringify(fixed)).not.toContain("s3cret-token");
});

test("declaration updates and deletion reject stale content digests", async ({ admin }) => {
  const server = created[0];
  const changed = expectStatus(
    await admin.patch<McpServer>(`/api/mcp/servers/${server.id}`, {
      declaration: { ...server.declaration!, description: "edited" },
      expected_digest: server.content_digest,
    }),
    200,
    "update MCP declaration",
  );
  expect(changed.declaration?.description).toBe("edited");
  expect(
    (
      await admin.patch(`/api/mcp/servers/${server.id}`, {
        declaration: { ...changed.declaration!, description: "stale" },
        expected_digest: server.content_digest,
      })
    ).status,
  ).toBe(409);
  expect(
    (
      await admin.delete(
        `/api/mcp/servers/${server.id}?expected_digest=${server.content_digest}`,
      )
    ).status,
  ).toBe(409);
  expect(
    (
      await admin.delete(
        `/api/mcp/servers/${server.id}?expected_digest=${changed.content_digest}`,
      )
    ).status,
  ).toBe(204);
  created.splice(0, 1);
});

test("an agent calls the remote tool through one shared session @model", async ({ admin }) => {
  test.setTimeout(300_000);
  const server = created.find((item) => item.name === "e2e")
    ?? (await createMcpPlugin(admin, open, { name: "e2e" }));
  if (!created.includes(server)) created.push(server);
  const { modelRef } = await ensureProvider(admin);
  const agentId = await ensureAgent(admin, modelRef, "e2e-mcp-agent");
  await expect
    .poll(
      async () => {
        const tools = expectStatus(
          await admin.get<{ tools: AgentTool[]; }>(
            `/api/agents/${agentId}/tools`,
          ),
          200,
          "list agent tools",
        ).tools;
        return tools.some((tool) => tool.name.includes("_main_add_"));
      },
      { timeout: 30_000 },
    )
    .toBe(true);
  const toolName = expectStatus(
    await admin.get<{ tools: AgentTool[]; }>(`/api/agents/${agentId}/tools`),
    200,
    "list agent tools",
  ).tools.find((tool) => tool.name.includes("_main_add_"))?.name;
  if (!toolName) throw new Error("file-backed add tool missing");
  const sessionID = await createChatSession(admin, agentId);
  const before = open.calls.length;
  const turn = await sendTurn(
    admin,
    agentId,
    sessionID,
    `Use ${toolName} with a=17 and b=25. Reply with only the result.`,
  );
  expect(turn.errors, JSON.stringify(turn.events.slice(-5))).toEqual([]);
  expect(turn.text).toContain("42");
  expect(
    open.calls
      .slice(before)
      .some(
        (call) => call.tool === "add" && call.args.a === 17 && call.args.b === 25,
      ),
  ).toBe(true);
});
