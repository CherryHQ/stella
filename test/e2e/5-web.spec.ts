// PR #1237: browser coverage for the MCP marketplace, drawer, scoped install,
// and the shared tool-permission surface.
import { createChatSession, ensureAgent, invokedToolNames, sendTurn, sessionMessages } from "../lib/agent.ts";
import { expectStatus } from "../lib/api.ts";
import { expect, test } from "../lib/fixtures.ts";
import { type McpFixture, startMcpFixture } from "../lib/mcp-fixture.ts";
import { type OAuthFixture, startOAuthFixture } from "../lib/oauth-fixture.ts";
import { ensureProvider } from "../lib/provider.ts";
import { loadRegistryFixtureState } from "../lib/registry-fixture.ts";
import { Server } from "../lib/types.ts";

test.describe.configure({ mode: "serial" });

const registry = loadRegistryFixtureState();
let oauthAS: OAuthFixture;
let oauthMcp: McpFixture;
let agentId = "";
let agentServerId = "";
const created: string[] = [];

async function server(admin: import("../lib/api.ts").ApiClient, id: string, scope = "user", agentId?: string): Promise<Server> {
  const query = new URLSearchParams({ scope });
  if (agentId) query.set("agent_id", agentId);
  return expectStatus(await admin.get<Server>(`/api/mcp/servers/${id}?${query}`), 200, `get ${id}`);
}

async function chooseScope(page: import("@playwright/test").Page, label: string) {
  await page.getByRole("radio", { name: new RegExp(label) }).check();
  await page.getByRole("button", { name: /^(Install|Add server)$/ }).last().click();
}

async function openManual(page: import("@playwright/test").Page) {
  await page.getByRole("button", { name: "Add server" }).first().click();
  const sheet = page.getByRole("dialog").last();
  await sheet.getByRole("button", { name: /^(Manual|手动)$/ }).click();
  await sheet.getByRole("button", { name: /set up manually|手动配置/i }).click();
  return page.getByRole("dialog").last();
}

async function fillManual(
  page: import("@playwright/test").Page,
  name: string,
  url: string,
  auth: "none" | "oauth" = "none",
) {
  const form = page.getByRole("dialog").last();
  await form.getByLabel("Name").fill(name);
  await form.getByLabel("URL").fill(url);
  if (auth === "oauth") {
    await form.getByRole("combobox").last().click();
    await page.getByRole("option", { name: "OAuth 2.1" }).click();
  }
  return form;
}

test.beforeAll(async () => {
  oauthAS = await startOAuthFixture();
  oauthMcp = await startMcpFixture({
    protectedResourceMetadata: `${oauthAS.url}/.well-known/oauth-protected-resource`,
    bearerValidator: (token) => oauthAS.issuedAccessTokens.has(token) && !oauthAS.revokedAccessTokens.has(token),
  });
  oauthAS.resource = oauthMcp.url;
});

test.afterAll(async ({ admin }) => {
  for (const id of created) await admin.delete(`/api/mcp/servers/${id}`);
  await oauthMcp.close();
  await oauthAS.close();
});

test("marketplace search, detail, global scope, install provenance, and next page", async ({ page, admin, db, loginAsAdmin }) => {
  await loginAsAdmin();
  await page.goto("/admin/resources/mcp");
  await page.getByRole("button", { name: "Add server" }).first().click();
  const sheet = page.getByRole("dialog").last();
  const search = sheet.getByPlaceholder("Search the MCP registry…");
  await search.fill("registry-add");
  await expect(sheet.getByRole("button", { name: /com\.stella\/registry-add/ })).toBeVisible();
  await expect(sheet.getByText("No auth", { exact: true })).toBeVisible();
  await sheet.locator("[data-slot='scroll-area-viewport'], .overflow-y-auto").last().evaluate((el) => el.scrollTo(0, el.scrollHeight));
  await expect(sheet.getByRole("button", { name: /com\.stella\/unsupported/ })).toBeVisible();
  await sheet.getByRole("button", { name: /com\.stella\/registry-add/ }).click();
  await expect(sheet.getByText("Connection URL", { exact: true })).toBeVisible();
  await sheet.getByRole("button", { name: "Install" }).click();
  await expect(sheet.getByRole("radio", { name: /Mine.*all agents/ })).toBeVisible();
  await expect(sheet.getByRole("radio", { name: /System.*all agents/ })).toBeVisible();
  await chooseScope(page, "Mine.*all agents");
  await expect(page.getByRole("heading", { name: "com.stella/registry-add", exact: true })).toBeVisible();

  const installedRow =
    (await db`select id, status, tools, metadata, name, url, scope from mcp_server where name = 'com.stella/registry-add' order by created_at desc limit 1`)[
      0
    ];
  expect(installedRow).toBeDefined();
  const installed = await server(admin, String(installedRow.id), "user");
  created.push(installed.id);
  await expect.poll(async () => String((await db`select status from mcp_server where id = ${installed.id}`)[0]?.status), {
    timeout: 15_000,
  }).toBe("ok");
  const probed = await server(admin, installed.id, "user");
  expect(probed.status).toBe("ok");
  expect(probed.tools?.map((tool) => tool.name).sort()).toEqual(["add", "echo"]);
  const row = (await db`select metadata, status, tools from mcp_server where id = ${installed!.id}`)[0];
  expect(row.status).toBe("ok");
  expect(row.metadata).toMatchObject({ registry: { source: "official", id: "com.stella/registry-add", version: "1.0.0" } });
  expect((row.tools as { name: string; }[]).map((tool) => tool.name).sort()).toEqual(["add", "echo"]);
});

test("bearer secret uses the registry template and only creates vault-backed material", async ({ page, admin, db, loginAsAdmin }) => {
  await loginAsAdmin();
  await page.goto("/admin/resources/mcp");
  await page.getByRole("button", { name: "Add server" }).first().click();
  const sheet = page.getByRole("dialog").last();
  await sheet.getByPlaceholder("Search the MCP registry…").fill("anything");
  const card = sheet.locator("div.flex.flex-col.gap-3.rounded-lg.border").filter({ hasText: "com.stella/bearer" });
  await card.getByRole("button", { name: "Install" }).click();
  await expect(sheet.getByText("Connection URL", { exact: true })).toBeVisible();
  await sheet.getByRole("button", { name: "Install" }).click();
  await expect(sheet.locator("label").filter({ hasText: "Bearer {api_key}" })).toBeVisible();
  const secret = sheet.locator('input[type="password"]').last();
  await expect(secret).toHaveAttribute("type", "password");
  await secret.fill("browser-bearer-secret");
  await sheet.getByRole("button", { name: "Next" }).click();
  await sheet.getByRole("radio", { name: /Mine.*all agents/ }).check();
  await sheet.getByRole("button", { name: "Install" }).last().click();

  const dbRow =
    (await db`select id, scope, agent_id, credential_ref, row_to_json(mcp_server)::text as raw from mcp_server where name = 'com.stella/bearer' order by created_at desc limit 1`)[
      0
    ];
  expect(dbRow).toBeDefined();
  const installed = await server(admin, String(dbRow.id), String(dbRow.scope), dbRow.agent_id ? String(dbRow.agent_id) : undefined);
  created.push(installed.id);
  expect(JSON.stringify(installed)).not.toContain("browser-bearer-secret");
  expect(dbRow.credential_ref).toBeTruthy();
  expect(String(dbRow.raw)).not.toContain("browser-bearer-secret");
  const vault = await db`select count(*)::int as n from vault_entry where name = ${dbRow.credential_ref as string}`;
  expect(Number(vault[0].n)).toBe(1);
  expect(await page.locator("body").textContent()).not.toContain("browser-bearer-secret");
});

test("unsupported registry entry hands off to the prefilled manual form", async ({ page, admin, db, loginAsAdmin }) => {
  await loginAsAdmin();
  await page.goto("/admin/resources/mcp");
  await page.getByRole("button", { name: "Add server" }).first().click();
  const sheet = page.getByRole("dialog").last();
  await sheet.getByPlaceholder("Search the MCP registry…").fill("anything");
  await sheet.locator("div.flex.flex-col.gap-3.rounded-lg.border").filter({ hasText: "com.stella/unsupported" }).getByRole("button", {
    name: "Install",
  }).click();
  await page.getByRole("dialog").last().getByRole("button", { name: "Install" }).click();
  const manualName = page.getByPlaceholder("github");
  const manualURL = page.getByPlaceholder("https://mcp.example.com/mcp");
  await expect(manualName).toHaveValue("com.stella/unsupported");
  await expect(manualURL).toHaveValue("http://127.0.0.1:1/unsupported");
  await page.getByRole("button", { name: "Add server" }).last().click();
  const rows = expectStatus(await admin.get<{ servers: Server[]; }>("/api/mcp/servers?scope=system"), 200, "list manual server");
  const manual = rows.servers.find((item) => item.name === "com.stella/unsupported");
  expect(manual).toBeDefined();
  created.push(manual!.id);
  expect((await db`select name, url from mcp_server where id = ${manual!.id}`)[0]).toMatchObject({
    name: "com.stella/unsupported",
    url: "http://127.0.0.1:1/unsupported",
  });
});

test("OAuth connect and disconnect run through the browser", async ({ page, admin, loginAsAdmin }) => {
  await loginAsAdmin();
  const createdOAuth = expectStatus(
    await admin.post<Server>("/api/mcp/servers", {
      scope: "user",
      name: "browser-oauth",
      url: oauthMcp.url,
      transport: "streamable_http",
      auth_type: "oauth",
    }),
    201,
    "create OAuth server",
  );
  created.push(createdOAuth.id);
  await page.goto("/settings/mcp");
  const row = await server(admin, createdOAuth.id);
  expect(row.status).toBe("needs_auth");
  const card = page.locator('[data-slot="card"]').filter({ hasText: "browser-oauth" });
  await expect(card.getByRole("button", { name: /Connect|连接/ })).toBeVisible();
  await card.getByRole("button", { name: /Connect|连接/ }).click();
  await page.waitForURL(/settings\/mcp/);
  await expect(page.getByText(/Connected|已连接/, { exact: true })).toBeVisible();
  await expect(card.getByRole("button", { name: /Disconnect|断开连接/ })).toBeVisible();
  await card.getByRole("button", { name: /Disconnect|断开连接/ }).click();
  await expect(card.getByRole("button", { name: /Connect|Reconnect|连接|重新连接/ })).toBeVisible();
  expect((await server(admin, row.id)).status).toBe("needs_auth");
  expect(oauthAS.counters.get("authorize")).toBeGreaterThanOrEqual(1);
});

test("drawer probes, edits and deletes with If-Match, and reloads stale conflicts", async ({ page, admin, db, loginAsAdmin }) => {
  await loginAsAdmin();
  const dead = expectStatus(
    await admin.post<Server>("/api/mcp/servers", {
      scope: "user",
      name: "drawer-dead",
      url: "http://127.0.0.1:9/mcp",
      transport: "streamable_http",
      auth_type: "none",
    }),
    201,
    "create dead",
  );
  created.push(dead.id);
  await page.goto("/settings/mcp");
  const card = page.locator('[data-slot="card"]').filter({ hasText: "drawer-dead" });
  await card.click();
  const drawer = page.getByRole("dialog");
  await expect(drawer.getByText("Error", { exact: true })).toBeVisible();
  await expect(drawer.getByText(dead.status_error!, { exact: true })).toBeVisible();
  expect(await drawer.textContent()).not.toMatch(/connection refused|dial tcp/);
  await expect(drawer.getByText("Tools", { exact: true })).toBeVisible();
  const before = (await server(admin, dead.id)).probed_at;
  await drawer.getByRole("button", { name: "Probe" }).click();
  await expect.poll(async () => (await server(admin, dead.id)).probed_at).not.toBe(before);

  await drawer.getByRole("button", { name: "Edit" }).click();
  const form = page.locator("body");
  await form.getByPlaceholder("github").fill("drawer-edited");
  const patch = page.waitForRequest((request) => request.method() === "PATCH" && request.url().includes(`/api/mcp/servers/${dead.id}`));
  await form.getByRole("button", { name: "Save" }).click();
  expect((await patch).headers()["if-match"]).toBe(dead.version);
  await expect(page.getByText("drawer-edited", { exact: true })).toBeVisible();

  const current = await server(admin, dead.id);
  expectStatus(
    await admin.patch(`/api/mcp/servers/${dead.id}?scope=user`, { name: "out-of-band" }, { "If-Match": current.version }),
    200,
    "out of band update",
  );
  expect((await server(admin, dead.id)).name).toBe("out-of-band");
  await page.getByText("drawer-edited", { exact: true }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Edit" }).click();
  const staleForm = page.locator("body");
  await staleForm.getByPlaceholder("github").fill("must-not-win");
  const staleResponse = page.waitForResponse((response) =>
    response.request().method() === "PATCH" && response.url().includes(`/api/mcp/servers/${dead.id}`)
  );
  await page.getByRole("button", { name: "Save" }).last().click();
  expect((await staleResponse).status()).toBe(409);
  await page.getByRole("button", { name: "Cancel" }).last().click({ force: true });
  await expect(page.getByText("out-of-band", { exact: true })).toBeVisible();
  await expect(page.getByText("must-not-win", { exact: true })).toHaveCount(0);
  expect((await server(admin, dead.id)).name).toBe("out-of-band");

  await page.getByText("out-of-band", { exact: true }).click();
  const deleteRequest = page.waitForRequest((request) =>
    request.method() === "DELETE" && request.url().includes(`/api/mcp/servers/${dead.id}`)
  );
  await page.getByRole("dialog").getByRole("button", { name: "Delete" }).click();
  await expect(page.getByRole("alertdialog")).toBeVisible();
  await page.getByRole("alertdialog").getByRole("button", { name: "Delete" }).click();
  expect((await deleteRequest).headers()["if-match"]).toBeTruthy();
  await expect.poll(async () => (await admin.get(`/api/mcp/servers/${dead.id}?scope=user`)).status).toBe(404);
  await page.reload();
  await expect(page.getByText("out-of-band", { exact: true })).toHaveCount(0);
});

test("agent-scoped install and MCP tool permission toggle persist", async ({ page, admin, db, loginAsAdmin }) => {
  const { modelRef } = await ensureProvider(admin);
  agentId = await ensureAgent(admin, modelRef, "e2e-mcp-web-agent");
  await loginAsAdmin();
  await page.goto(`/agents/${agentId}/profile?tab=tools`);
  await page.getByRole("button", { name: "Add MCP server" }).click();
  const sheet = page.getByRole("dialog");
  await sheet.getByRole("button", { name: /^(Manual|手动)$/ }).click();
  const form = page.locator("body");
  await form.getByPlaceholder("github").fill("agent-browser");
  await form.getByPlaceholder("https://mcp.example.com/mcp").fill(registry.mcpUrl);
  await form.getByRole("button", { name: "Next" }).click();
  await page.getByRole("radio", { name: /Mine.*this agent/ }).check();
  await page.getByRole("button", { name: "Add server" }).last().click();
  const scopedRow = (await db`select id, scope, agent_id from mcp_server where name = 'agent-browser' order by created_at desc limit 1`)[0];
  expect(scopedRow).toBeDefined();
  agentServerId = String(scopedRow.id);
  created.push(agentServerId);
  expect(String(scopedRow.agent_id)).toBe(agentId);
  expect(String(scopedRow.scope)).toBe("user_agent");
  await page.getByText("agent-browser", { exact: true }).click();
  await expect(page.getByText("mcp__agent_browser__add", { exact: true })).toBeVisible();
  const tool = page.locator('[data-slot="card"]').filter({ hasText: "mcp__agent_browser__add" });
  await tool.getByRole("switch").click();
  await expect.poll(async () =>
    (await db`select enabled from tool_override where tool_name = 'mcp__agent_browser__add' and scope = 'user_agent' and agent_id = ${agentId}`)[
      0
    ]?.enabled
  ).toBe(false);
});

test("a real agent calls add on the browser-installed server @model", async ({ admin }) => {
  test.setTimeout(300_000);
  if (!agentServerId) {
    const { modelRef } = await ensureProvider(admin);
    agentId = await ensureAgent(admin, modelRef, "e2e-mcp-web-agent");
    const setup = await admin.post<Server>("/api/mcp/servers", {
      scope: "user_agent",
      agent_id: agentId,
      name: "agent-browser",
      url: registry.mcpUrl,
      transport: "streamable_http",
      auth_type: "none",
    });
    if (setup.status === 201) {
      agentServerId = setup.body.id;
      created.push(agentServerId);
    } else if (setup.status === 409) {
      const match = JSON.stringify(setup.body).match(/id ([0-9a-f-]{36})/i);
      if (!match) throw new Error(`could not recover existing model browser server: ${JSON.stringify(setup.body)}`);
      agentServerId = match[1];
    } else {
      throw new Error(`create model browser server: ${setup.status}`);
    }
  }
  expect(agentServerId).toBeTruthy();
  const scoped = await server(admin, agentServerId, "user_agent", agentId);
  expectStatus(
    await admin.patch(`/api/agents/${agentId}/tools/mcp__agent_browser__add`, { enabled: true, scope: "user_agent" }),
    200,
    "enable add",
  );
  const session = await createChatSession(admin, agentId);
  const turn = await sendTurn(admin, agentId, session, "Call mcp__agent_browser__add with a=17 and b=25. Reply with only the result.");
  expect(turn.errors, JSON.stringify(turn.events.slice(-5))).toEqual([]);
  expect(turn.text).toContain("42");
  expect(invokedToolNames(await sessionMessages(admin, agentId, session))).toContain("mcp__agent_browser__add");
});
