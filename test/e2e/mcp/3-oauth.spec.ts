// PR #1235: OAuth 2.1 authorization-code + PKCE for remote MCP servers.
import { OAuthState, Server } from "../lib/types.ts";
import { expectStatus } from "../lib/api.ts";
import { createChatSession, ensureAgent, invokedToolNames, sendTurn, sessionMessages } from "../lib/agent.ts";
import { expect, loginWithPassword, test } from "../lib/fixtures.ts";
import { expireAccessToken, setTokenFailure, startOAuthFixture, tokenHits, type OAuthFixture } from "../lib/oauth-fixture.ts";
import { startMcpFixture, type McpFixture } from "../lib/mcp-fixture.ts";
import { ensureProvider } from "../lib/provider.ts";
import type { ApiClient } from "../lib/api.ts";


 test.describe.configure({ mode: "serial" });

let as: OAuthFixture;
let mcp: McpFixture;
const created: string[] = [];
let oauthServer: Server;
let agentID = "";

async function connect(api: ApiClient, server: Server): Promise<{ flowID: string; callback: Response }> {
  const started = expectStatus(await api.post<{ authorization_url: string; flow_id: string }>(`/api/mcp/servers/${server.id}/oauth-start`), 201, "start OAuth");
  const approved = await fetch(started.authorization_url, { redirect: "manual" });
  const location = approved.headers.get("location");
  expect(approved.status).toBe(302);
  expect(location).toBeTruthy();
  return { flowID: started.flow_id, callback: await fetch(location!, { redirect: "manual" }) };
}

async function getServer(api: ApiClient, id: string): Promise<Server> {
  return expectStatus(await api.get<Server>(`/api/mcp/servers/${id}`), 200, "get OAuth server");
}

function vaultName(prefix: string, id: string): string {
  return `${prefix}${id.replaceAll("-", "_").toUpperCase()}`;
}

async function vaultCount(db: import("../lib/db.ts").Sql, name: string): Promise<number> {
  const rows = await db`select count(*)::int as n from vault_entry where name = ${name}`;
  return Number(rows[0].n);
}

test.beforeAll(async () => {
  as = await startOAuthFixture();
  mcp = await startMcpFixture({
    protectedResourceMetadata: `${as.url}/.well-known/oauth-protected-resource`,
    bearerValidator: (token) => as.issuedAccessTokens.has(token) && !as.revokedAccessTokens.has(token),
  });
  as.resource = mcp.url;
});

test.afterAll(async ({ admin }) => {
  for (const id of created) await admin.delete(`/api/mcp/servers/${id}`);
  await mcp.close();
  await as.close();
});

test("API + DB complete the PKCE flow and persist only a vault bundle", async ({ admin, db }) => {
  oauthServer = expectStatus(await admin.post<Server>("/api/mcp/servers", {
    scope: "user", name: "oauth-e2e", url: mcp.url, transport: "streamable_http", auth_type: "oauth",
  }), 201, "create OAuth server");
  created.push(oauthServer.id);
  expect(oauthServer.status, JSON.stringify(oauthServer)).toBe("needs_auth");
  expect(oauthServer.oauth).toMatchObject({ connected: false, needs_reconnect: false, client_registered: false });

  const started = expectStatus(await admin.post<{ authorization_url: string; flow_id: string; expires_at: string }>(`/api/mcp/servers/${oauthServer.id}/oauth-start`), 201, "start OAuth");
  expect(started.authorization_url).toContain("code_challenge=");
  const flows = await db`select server_id, user_id, pkce_verifier, consumed_at from mcp_oauth_flow where id = ${started.flow_id}`;
  expect(flows).toHaveLength(1);
  expect(flows[0].server_id).toBe(oauthServer.id);
  expect(flows[0].pkce_verifier).toBeTruthy();
  expect(flows[0].consumed_at).toBeNull();

  const approved = await fetch(started.authorization_url, { redirect: "manual" });
  const callbackURL = approved.headers.get("location");
  expect(approved.status).toBe(302);
  expect(callbackURL).toContain("code=");
  expect(callbackURL).toContain("state=");
  const callback = await fetch(callbackURL!, { redirect: "manual" });
  expect(callback.status, await callback.text()).toBe(302);
  expect(callback.headers.get("location")).toContain("connected=");

  const connected = await getServer(admin, oauthServer.id);
  expect(connected.status).toBe("ok");
  expect(connected.oauth?.connected).toBe(true);
  expect(connected.tools?.map((tool) => tool.name)).toEqual(["add", "echo"]);
  expect(JSON.stringify(connected)).not.toContain("e2e-access");
  expect(JSON.stringify(connected)).not.toContain("e2e-refresh");
  expect(await vaultCount(db, vaultName("MCP_OAUTH_", oauthServer.id))).toBe(1);
  expect(await vaultCount(db, vaultName("MCP_OAUTH_CLIENT_", oauthServer.id))).toBe(1);
  const flow = (await db`select consumed_at from mcp_oauth_flow where id = ${started.flow_id}`)[0];
  expect(flow.consumed_at).not.toBeNull();
  expect((as.counters.get("register") ?? 0)).toBe(1);

  const replay = await fetch(callbackURL!, { redirect: "manual" });
  expect(replay.status).toBe(302);
  expect(replay.headers.get("location")).toContain("oauth_error=expired");
});

test("expired flow is rejected and refresh is single-shot", async ({ admin, db }) => {
  const started = expectStatus(await admin.post<{ authorization_url: string; flow_id: string }>(`/api/mcp/servers/${oauthServer.id}/oauth-start`), 201, "start second OAuth");
  const expired = await db`update mcp_oauth_flow set expires_at = now() - interval '1 minute' where id = ${started.flow_id} returning id`;
  expect(expired).toHaveLength(1);
  const approved = await fetch(started.authorization_url, { redirect: "manual" });
  const callback = await fetch(approved.headers.get("location")!, { redirect: "manual" });
  expect(callback.headers.get("location")).toContain("oauth_error=expired");

  // A one-second access token is expired by the callback's real post-connect
  // probe, forcing exactly one refresh. The refresh response is long-lived.
  const before = tokenHits(as);
  as.expiresIn = 1;
  const fresh = await connect(admin, oauthServer);
  expect(fresh.callback.status).toBe(302);
  expect(tokenHits(as)).toBe(before + 2); // authorization-code exchange + refresh
  expect((await getServer(admin, oauthServer.id)).status).toBe("ok");
});

test("rejected access and refresh failure fail closed without a retry loop", async ({ admin }) => {
  expireAccessToken(as);
  const rejected = await admin.post<Server>(`/api/mcp/servers/${oauthServer.id}/probe`);
  expect(rejected.status).toBe(200);
  expect(rejected.body.status).toBe("needs_auth");
  expect(rejected.body.status_error).not.toContain("e2e-access");

  // Make the refreshed access token expire, then reject the refresh grant.
  setTokenFailure(as, 0, "");
  as.expiresIn = 1;
  as.refreshExpiresIn = 1;
  const reconnected = await connect(admin, oauthServer);
  expect(reconnected.callback.status).toBe(302);
  setTokenFailure(as, 400, JSON.stringify({ error: "invalid_grant" }));
  await new Promise((resolve) => setTimeout(resolve, 1100));
  const failed = await admin.post<Server>(`/api/mcp/servers/${oauthServer.id}/probe`);
  expect(failed.status).toBe(200);
  expect(failed.body.status).toBe("needs_auth");
  expect(failed.body.status_error).toContain("reconnect");
  const after = tokenHits(as);
  const again = await admin.post<Server>(`/api/mcp/servers/${oauthServer.id}/probe`);
  expect(tokenHits(as)).toBe(after);
  expect(again.body.status).toBe("needs_auth");
  setTokenFailure(as, 0, "");
  as.refreshExpiresIn = 3600;
});

test("disconnect removes the bundle and UI exposes Connect, Reconnect, Disconnect", async ({ admin, db, page, loginAsAdmin }) => {
  const connected = await getServer(admin, oauthServer.id);
  const disconnected = expectStatus(await admin.post<Server>(`/api/mcp/servers/${oauthServer.id}/oauth-disconnect`), 200, "disconnect OAuth");
  expect(disconnected.status).toBe("needs_auth");
  expect(disconnected.oauth?.connected).toBe(false);
  expect(await vaultCount(db, vaultName("MCP_OAUTH_", oauthServer.id))).toBe(0);

  await loginAsAdmin();
  await page.goto("/settings/mcp");
  const card = page.locator('[data-slot="card"]').filter({ hasText: "oauth-e2e" });
  await expect(card.getByRole("button", { name: /重新连接|Reconnect/ })).toBeVisible();
  expect(connected.oauth?.connected).toBe(true);
  const fresh = expectStatus(await admin.post<Server>("/api/mcp/servers", {
    scope: "user", name: "oauth-ui-connect", url: mcp.url.replace("/mcp", "/ui-connect"), transport: "streamable_http", auth_type: "oauth",
  }), 201, "create UI connect server");
  created.push(fresh.id);
  await page.reload();
  const freshCard = page.locator('[data-slot="card"]').filter({ hasText: "oauth-ui-connect" });
  await expect(freshCard.getByRole("button", { name: /连接|Connect/ })).toBeVisible();
  await card.getByRole("button", { name: /重新连接|Reconnect/ }).click();
  await page.waitForURL(/settings\/mcp\?connected=/);
  await expect(page.getByText(/已连接|Connected/, { exact: true })).toBeVisible();
  await expect(card.getByRole("button", { name: /断开连接|Disconnect/ })).toBeVisible();
});

test("per-user bundles isolate users and a real agent calls OAuth MCP @model", async ({ admin, user, db }) => {
  const { modelRef } = await ensureProvider(admin);
  agentID = await ensureAgent(admin, modelRef, "e2e-oauth-agent");
  const perUser = expectStatus(await admin.post<Server>("/api/mcp/servers", {
    scope: "system", name: "oauth-per-user", url: mcp.url, transport: "streamable_http", auth_type: "oauth", credential_mode: "per_user",
  }), 201, "create per-user OAuth server");
  created.push(perUser.id);
  expect(perUser.status).toBe("needs_auth");
  expect(perUser.credential_mode).toBe("per_user");

  const beforeConnect = await user.get<{ tools: { name: string; availability_reason?: string }[] }>(`/api/agents/${agentID}/tools`);
  expect(beforeConnect.status).toBe(200);

  const adminStart = await connect(admin, perUser);
  expect(adminStart.callback.status).toBe(302);
  const userStillNeedsAuth = await user.get<{ tools: { name: string; availability_reason?: string }[] }>(`/api/agents/${agentID}/tools`);
  expect(userStillNeedsAuth.body.tools.some((tool) => tool.name === "mcp__oauth_per_user__add" && tool.availability_reason === "mcp_needs_auth"), JSON.stringify(userStillNeedsAuth.body)).toBe(true);

  const userStart = await connect(user, perUser);
  expect(userStart.callback.status).toBe(302);
  const bundleRows = await db`select scope, user_id from vault_entry where name = ${vaultName("MCP_OAUTH_", perUser.id)} order by user_id`;
  expect(bundleRows).toHaveLength(2);
  expect(new Set(bundleRows.map((row) => String(row.user_id))).size).toBe(2);

  const session = await createChatSession(admin, agentID);
  const turn = await sendTurn(admin, agentID, session, "Call mcp__oauth_per_user__add with a=17 and b=25. Reply with only the result.");
  expect(turn.errors, JSON.stringify(turn.events.slice(-5))).toEqual([]);
  expect(turn.text).toContain("42");
  expect(mcp.calls.some((call) => call.tool === "add" && call.args.a === 17 && call.args.b === 25)).toBe(true);
  expect(invokedToolNames(await sessionMessages(admin, agentID, session))).toContain("mcp__oauth_per_user__add");
});
