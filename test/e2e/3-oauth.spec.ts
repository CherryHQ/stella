// OAuth 2.1 authorization-code + PKCE for file-backed MCP servers.
import { expectStatus } from "./lib/api.ts";
import type { ApiClient } from "./lib/api.ts";
import { expect, test } from "./lib/fixtures.ts";
import { createMcpPlugin, deleteMcpServer, type McpFixture, startMcpFixture } from "./lib/mcp-fixture.ts";
import { expireAccessToken, type OAuthFixture, setTokenFailure, startOAuthFixture, tokenHits } from "./lib/oauth-fixture.ts";
import type { McpServer } from "./lib/types.ts";

test.describe.configure({ mode: "serial" });
let as: OAuthFixture;
let mcp: McpFixture;
const created: McpServer[] = [];

async function startFlow(api: ApiClient, server: McpServer): Promise<Response> {
  const current = expectStatus(
    await api.get<McpServer>(`/api/mcp/servers/${server.id}`),
    200,
    "refresh OAuth server digest",
  );
  const started = expectStatus(
    await api.post<{
      authorization_url: string;
      flow_id: string;
      expires_at: string;
    }>(`/api/mcp/servers/${server.id}/oauth/start`, {
      expected_digest: current.content_digest,
    }),
    201,
    "start OAuth",
  );
  expect(started.authorization_url).toContain("code_challenge=");
  const approved = await fetch(started.authorization_url, {
    redirect: "manual",
  });
  expect(approved.status).toBe(302);
  const callbackURL = approved.headers.get("location");
  expect(callbackURL).toContain("code=");
  expect(callbackURL).toContain("state=");
  return fetch(callbackURL!, { redirect: "manual" });
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
  for (const server of created) await deleteMcpServer(admin, server);
  await mcp.close();
  await as.close();
});

test("API completes PKCE and exposes only safe OAuth state", async ({ admin }) => {
  const server = await createMcpPlugin(admin, mcp, {
    name: "oauth-e2e",
    authType: "oauth",
    credentialMode: "per_user",
  });
  created.push(server);
  const initial = expectStatus(
    await admin.post<McpServer>(`/api/mcp/servers/${server.id}/probe`),
    200,
    "probe OAuth server",
  );
  expect(initial.status).toBe("needs_auth");
  const callback = await startFlow(admin, server);
  expect(callback.status, await callback.text()).toBe(302);
  expect(callback.headers.get("location")).toContain("connected=");
  const connected = expectStatus(
    await admin.get<McpServer>(`/api/mcp/servers/${server.id}`),
    200,
    "get connected server",
  );
  expect(connected.declaration).toMatchObject({ auth_type: "oauth" });
  // OAuth completion persists the grant, but file-backed probe observations
  // stay disposable until the next explicit probe.
  expect(connected.status).toBe("unknown");
  expect(connected.needs_auth).toBe(false);
  expect(JSON.stringify(connected)).not.toContain("e2e-access");
  expect(JSON.stringify(connected)).not.toContain("e2e-refresh");
  expect(as.counters.get("register") ?? 0).toBeGreaterThanOrEqual(1);
});

test("an expired OAuth token refreshes once on explicit probes", async ({ admin }) => {
  const server = created[0];
  as.expiresIn = 1;
  const refreshed = await startFlow(admin, server);
  expect(refreshed.status).toBe(302);
  await new Promise((resolve) => setTimeout(resolve, 1_200));
  const before = tokenHits(as);
  const firstProbe = expectStatus(
    await admin.post<McpServer>(`/api/mcp/servers/${server.id}/probe`),
    200,
    "probe expired OAuth token",
  );
  expect(firstProbe.status).toBe("ready");
  expect(tokenHits(as)).toBe(before + 1);
  const secondProbe = expectStatus(
    await admin.post<McpServer>(`/api/mcp/servers/${server.id}/probe`),
    200,
    "repeat probe after OAuth refresh",
  );
  expect(secondProbe.status).toBe("ready");
  expect(tokenHits(as)).toBe(before + 1);
});

test("revoked access and refresh failure fail closed", async ({ admin }) => {
  const server = created[0];
  expireAccessToken(as);
  setTokenFailure(
    as,
    503,
    JSON.stringify({ error: "temporarily unavailable" }),
  );
  const rejected = expectStatus(
    await admin.post<McpServer>(`/api/mcp/servers/${server.id}/probe`),
    200,
    "probe expired OAuth token",
  );
  expect(["needs_auth", "error"]).toContain(rejected.status);
  const failedRefresh = expectStatus(
    await admin.post<McpServer>(`/api/mcp/servers/${server.id}/probe`),
    200,
    "probe with failed OAuth refresh",
  );
  expect(["needs_auth", "error"]).toContain(failedRefresh.status);
  setTokenFailure(as, 0, "");
  const disconnected = expectStatus(
    await admin.post<McpServer>(
      `/api/mcp/servers/${server.id}/oauth/disconnect`,
    ),
    200,
    "disconnect OAuth",
  );
  expect(disconnected.status).toBe("unknown");
  expect(disconnected.needs_auth).toBe(true);
});

test("per-user OAuth grants stay separate for admin and user", async ({ admin, user }) => {
  const server = await createMcpPlugin(admin, mcp, {
    name: "oauth-per-user",
    authType: "oauth",
    credentialMode: "per_user",
    scope: "system",
  });
  created.push(server);
  expectStatus(
    await admin.post<McpServer>(`/api/mcp/servers/${server.id}/probe`),
    200,
    "probe per-user server",
  );
  const adminCallback = await startFlow(admin, server);
  expect(adminCallback.status).toBe(302);
  const userServer = expectStatus(
    await user.get<McpServer>(`/api/mcp/servers/${server.id}`),
    200,
    "read per-user server",
  );
  const userCallback = await startFlow(user, userServer);
  expect(userCallback.status).toBe(302);
  expect(as.counters.get("authorize") ?? 0).toBeGreaterThanOrEqual(2);
  const disconnected = expectStatus(
    await user.post<McpServer>(
      `/api/mcp/servers/${server.id}/oauth/disconnect`,
    ),
    200,
    "disconnect user OAuth",
  );
  expect(disconnected.status).toBe("unknown");
  expect(disconnected.needs_auth).toBe(true);
});
