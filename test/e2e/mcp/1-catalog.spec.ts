// PR #1233: persisted tool catalog, probe status, shared session per server,
// vault-backed bearer credentials, and If-Match optimistic concurrency.
import { expectStatus } from "../lib/api.ts";
import { createChatSession, ensureAgent, invokedToolNames, sendTurn, sessionMessages } from "../lib/agent.ts";
import { expect, test } from "../lib/fixtures.ts";
import { startMcpFixture, type McpFixture } from "../lib/mcp-fixture.ts";
import { ensureProvider } from "../lib/provider.ts";

interface McpServer {
  id: string;
  name: string;
  url: string;
  scope: string;
  status: string;
  status_error?: string;
  probed_at: string | null;
  tools?: { name: string }[];
  auth_type: string;
  enabled: boolean;
  version: string;
}

test.describe.configure({ mode: "serial" });

let open: McpFixture;
let guarded: McpFixture;
const created: string[] = [];

test.beforeAll(async () => {
  open = await startMcpFixture();
  guarded = await startMcpFixture({ bearer: "s3cret-token" });
});

test.afterAll(async ({ admin }) => {
  for (const id of created) await admin.delete(`/api/mcp/servers/${id}`);
  await open.close();
  await guarded.close();
});

test("create probes the server and persists its catalog", async ({ admin, db }) => {
  const body = expectStatus(
    await admin.post<McpServer>("/api/mcp/servers", {
      scope: "user",
      name: "e2e",
      url: open.url,
      transport: "streamable_http",
      auth_type: "none",
    }),
    201,
    "create server",
  );
  created.push(body.id);
  expect(body.status).toBe("ok");
  expect(body.probed_at).not.toBeNull();
  expect(body.version).toBeTruthy();
  expect((body.tools ?? []).map((t) => t.name).sort()).toEqual(["add", "echo"]);
  expect(open.methods.get("initialize")).toBeGreaterThanOrEqual(1);
  expect(open.methods.get("tools/list")).toBeGreaterThanOrEqual(1);

  const rows = await db`select status, status_error, probed_at, tools, credential_mode from mcp_server where id = ${body.id}`;
  expect(rows).toHaveLength(1);
  expect(rows[0].status).toBe("ok");
  expect(rows[0].status_error).toBe("");
  expect(rows[0].probed_at).not.toBeNull();
  expect(rows[0].credential_mode).toBe("shared");
  expect((rows[0].tools as { name: string }[]).map((t) => t.name).sort()).toEqual(["add", "echo"]);

  const fetched = expectStatus(await admin.get<McpServer>(`/api/mcp/servers/${body.id}`), 200, "get server");
  expect(fetched.tools?.length).toBe(2);
  const list = expectStatus(await admin.get<{ servers: McpServer[] }>("/api/mcp/servers"), 200, "list servers");
  expect(list.servers.some((s) => s.id === body.id && s.status === "ok")).toBe(true);
});

test("probe endpoint re-lists tools and refreshes probed_at", async ({ admin, db }) => {
  const id = created[0];
  const before = (await db`select probed_at from mcp_server where id = ${id}`)[0].probed_at as Date;
  const lists = open.methods.get("tools/list") ?? 0;
  await new Promise((r) => setTimeout(r, 20));
  const body = expectStatus(await admin.post<McpServer>(`/api/mcp/servers/${id}/probe`), 200, "probe");
  expect(body.status).toBe("ok");
  expect(open.methods.get("tools/list")).toBe(lists + 1);
  const after = (await db`select probed_at from mcp_server where id = ${id}`)[0].probed_at as Date;
  expect(after.getTime()).toBeGreaterThan(before.getTime());
});

test("unreachable endpoint records an error and an empty catalog", async ({ admin, db }) => {
  const body = expectStatus(
    await admin.post<McpServer>("/api/mcp/servers", {
      scope: "user",
      name: "e2e-dead",
      url: "http://127.0.0.1:9/mcp",
      transport: "streamable_http",
    }),
    201,
    "create dead server",
  );
  created.push(body.id);
  expect(body.status).toBe("error");
  expect(body.probed_at).not.toBeNull();
  // The reason names the registration, not the transport-level cause.
  expect(body.status_error).toContain('"e2e-dead"');
  expect(body.status_error).not.toMatch(/connection refused|dial tcp/);
  expect(body.tools ?? []).toHaveLength(0);
  const row = (await db`select status, status_error, tools from mcp_server where id = ${body.id}`)[0];
  expect(row.status).toBe("error");
  expect(String(row.status_error)).toBe(body.status_error);
  expect(row.tools).toEqual([]);
});

test("bearer token lives in the vault and a 401 flips the server to needs_auth", async ({ admin, db }) => {
  const body = expectStatus(
    await admin.post<McpServer>("/api/mcp/servers", {
      scope: "user",
      name: "e2e-guarded",
      url: guarded.url,
      transport: "streamable_http",
      auth_type: "bearer",
      token: "wrong-token",
    }),
    201,
    "create guarded server",
  );
  created.push(body.id);
  expect(body.status).toBe("needs_auth");
  expect(JSON.stringify(body)).not.toContain("wrong-token");

  const row = (await db`select credential_ref, row_to_json(mcp_server)::text as raw from mcp_server where id = ${body.id}`)[0];
  expect(row.credential_ref).toBeTruthy();
  expect(String(row.raw)).not.toContain("wrong-token");
  const vault = await db`select count(*)::int as n from vault_entry where name = ${row.credential_ref as string}`;
  expect(vault[0].n).toBe(1);

  const fixed = expectStatus(
    await admin.patch<McpServer>(
      `/api/mcp/servers/${body.id}`,
      { auth_type: "bearer", token: "s3cret-token" },
      { "If-Match": body.version },
    ),
    200,
    "patch token",
  );
  expect(fixed.status).toBe("ok");
  expect((fixed.tools ?? []).map((t) => t.name)).toContain("add");
});

test("If-Match enforces optimistic concurrency on PATCH and DELETE", async ({ admin }) => {
  const id = created[0];
  const current = expectStatus(await admin.get<McpServer>(`/api/mcp/servers/${id}`), 200, "get");
  const stale = await admin.patch(`/api/mcp/servers/${id}`, { enabled: true }, { "If-Match": "stale-version" });
  expect(stale.status).toBe(409);
  const ok = expectStatus(
    await admin.patch<McpServer>(`/api/mcp/servers/${id}`, { enabled: true }, { "If-Match": current.version }),
    200,
    "patch with current version",
  );
  expect(ok.version).toBeTruthy();

  const victim = expectStatus(
    await admin.post<McpServer>("/api/mcp/servers", { scope: "user", name: "e2e-victim", url: open.url }),
    201,
    "create victim",
  );
  const staleDelete = await admin.delete(`/api/mcp/servers/${victim.id}`, { "If-Match": "stale-version" });
  expect(staleDelete.status).toBe(409);
  const del = await admin.delete(`/api/mcp/servers/${victim.id}`, { "If-Match": victim.version });
  expect(del.status).toBe(204);
});

test("an agent calls the remote tool through one shared session", async ({ admin, db }) => {
  test.setTimeout(300_000);
  const { modelRef } = await ensureProvider(admin);
  const agentId = await ensureAgent(admin, modelRef);
  const sessionId = await createChatSession(admin, agentId);
  const initBefore = open.methods.get("initialize") ?? 0;
  const callsBefore = open.calls.length;

  const turn = await sendTurn(
    admin,
    agentId,
    sessionId,
    "Use the tool mcp__e2e__add twice: first with a=17 and b=25, then with a=3 and b=4. Reply with only the two results separated by a space.",
  );
  expect(turn.errors, JSON.stringify(turn.events.slice(-5))).toEqual([]);
  const addCalls = turn.toolCalls.filter((c) => c.toolName === "mcp__e2e__add");
  expect(addCalls.length).toBeGreaterThanOrEqual(2);
  const seen = open.calls.slice(callsBefore).filter((c) => c.tool === "add").map((c) => `${c.args.a}+${c.args.b}`);
  expect(seen).toContain("17+25");
  expect(seen).toContain("3+4");
  expect(turn.text).toContain("42");
  // Both proxies share one lazily opened session: at most one initialize per turn.
  expect((open.methods.get("initialize") ?? 0) - initBefore).toBeLessThanOrEqual(1);

  const rows = await db`
    select m.role, m.event_type, m.content from ctx_message m
    join ctx_conversation c on c.id = m.conversation_id
    where c.session_id = ${sessionId} order by m.seq`;
  const persistedCalls = rows.filter((r) => r.event_type === "tool_call" && String(r.content).includes("mcp__e2e__add"));
  expect(persistedCalls.length, JSON.stringify(rows.map((r) => [r.role, r.event_type, String(r.content).slice(0, 80)]))).toBeGreaterThanOrEqual(2);

  // The transcript API shows the call either as a direct tool_call block or,
  // under Code Mode, in the child-call audit of the outer `code` result.
  const messages = await sessionMessages(admin, agentId, sessionId);
  const invoked = invokedToolNames(messages).filter((n) => n === "mcp__e2e__add");
  expect(invoked.length, JSON.stringify(messages).slice(0, 3000)).toBeGreaterThanOrEqual(2);
});

test("settings page lists servers and can register one", async ({ page, admin, loginAsAdmin }) => {
  await loginAsAdmin();
  await page.goto("/settings/mcp");
  await expect(page.getByText("e2e", { exact: true })).toBeVisible();
  await expect(page.getByText(open.url).first()).toBeVisible();

  await page.getByRole("button", { name: "Add server" }).click();
  const sheet = page.getByRole("dialog");
  await sheet.getByPlaceholder("github").fill("e2e-ui");
  await sheet.getByPlaceholder("https://mcp.example.com/mcp").fill(open.url);
  await sheet.getByRole("button", { name: "Add server" }).click();
  await expect(page.getByText("e2e-ui", { exact: true })).toBeVisible();

  const list = expectStatus(await admin.get<{ servers: McpServer[] }>("/api/mcp/servers"), 200, "list");
  const ui = list.servers.find((s) => s.name === "e2e-ui");
  expect(ui).toBeDefined();
  created.push(ui!.id);
  expect(ui!.status).toBe("ok");
  expect(ui!.tools?.map((t) => t.name)).toContain("add");
});
