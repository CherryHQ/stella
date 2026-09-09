// Browser coverage for raw plugin files, MCP file resources, marketplace install,
// and the OAuth connect/disconnect controls.
import { expectStatus } from "./lib/api.ts";
import { expect, test } from "./lib/fixtures.ts";
import { createMcpPlugin, deleteMcpServer, type McpFixture, startMcpFixture } from "./lib/mcp-fixture.ts";
import { type OAuthFixture, startOAuthFixture } from "./lib/oauth-fixture.ts";
import { loadRegistryFixtureState } from "./lib/registry-fixture.ts";
import type { McpServer, PluginResource } from "./lib/types.ts";

test.describe.configure({ mode: "serial" });
const registry = loadRegistryFixtureState();
let oauthAS: OAuthFixture;
let oauthMcp: McpFixture;
let openMcp: McpFixture;
const createdServers: McpServer[] = [];
const createdPlugins: PluginResource[] = [];

async function openRegistry(page: import("@playwright/test").Page) {
  await page
    .getByRole("button", {
      name: /^(Add MCP server|Browse MCP registry|添加 MCP 服务器|浏览 MCP 注册表)$/,
    })
    .first()
    .click();
}

test.beforeAll(async () => {
  oauthAS = await startOAuthFixture();
  oauthMcp = await startMcpFixture({
    protectedResourceMetadata: `${oauthAS.url}/.well-known/oauth-protected-resource`,
    bearerValidator: (token) =>
      oauthAS.issuedAccessTokens.has(token)
      && !oauthAS.revokedAccessTokens.has(token),
  });
  oauthAS.resource = oauthMcp.url;
  openMcp = await startMcpFixture();
});
test.afterAll(async ({ admin }) => {
  for (const server of createdServers) await deleteMcpServer(admin, server);
  for (const plugin of createdPlugins) {
    const current = await admin.get<PluginResource>(
      `/api/plugins/${plugin.id}`,
    );
    if (current.status === 200) {
      await admin.delete(
        `/api/plugins/${plugin.id}?expected_digest=${encodeURIComponent(current.body.content_digest)}`,
      );
    }
  }
  await oauthMcp.close();
  await oauthAS.close();
  await openMcp.close();
});

test("marketplace search installs a scoped file-backed MCP resource", async ({ page, admin, loginAsAdmin }) => {
  await loginAsAdmin();
  await page.goto("/admin/resources/mcp");
  await openRegistry(page);
  const sheet = page.getByRole("dialog").last();
  await sheet.getByPlaceholder("Search the MCP registry…").fill("registry-add");
  await expect(
    sheet.getByRole("button", { name: /com\.stella\/registry-add/ }),
  ).toBeVisible();
  await sheet
    .getByRole("button", { name: /com\.stella\/registry-add/ })
    .click();
  await expect(
    sheet.getByText("Connection URL", { exact: true }),
  ).toBeVisible();
  await sheet.getByRole("button", { name: "Install" }).click();
  await expect(
    sheet.getByRole("radio", { name: /Mine.*all agents/ }),
  ).toBeVisible();
  await sheet.getByRole("radio", { name: /Mine.*all agents/ }).check();
  await sheet
    .getByRole("button", { name: /^(Install|安装)$/ })
    .last()
    .click();
  await expect(
    page.getByText(/MCP server installed|MCP 服务器已安装/).first(),
  ).toBeVisible();
  const listed = expectStatus(
    await admin.get<{ servers: McpServer[]; }>("/api/mcp/servers?scope=user"),
    200,
    "list installed MCP resources",
  );
  const installed = listed.servers.find((item) => item.name.includes("registry"));
  expect(installed).toBeDefined();
  if (installed) createdServers.push(installed);
});

test("MCP detail edits declaration and raw file with content digest", async ({ page, admin, loginAsAdmin }) => {
  // The admin route is the Global MCP inventory, whose default scope is
  // system. Keep the fixture in that scope so the resource is visible without
  // changing the page selector and accidentally masking a product bug.
  const server = await createMcpPlugin(admin, openMcp, {
    name: "browser-mcp",
    scope: "system",
  });
  createdServers.push(server);
  await loginAsAdmin();
  await page.goto("/admin/resources/mcp");
  // The card exposes both its display name and server key, which are equal for
  // this fixture and therefore produce two exact text nodes. Target the card
  // button's combined accessible name to keep strict mode meaningful.
  const browserMcpCard = page.getByRole("button", {
    name: "browser-mcp browser-mcp",
  });
  await expect(browserMcpCard).toBeVisible();
  await browserMcpCard.click();
  await expect(
    page.getByRole("button", { name: /JSON mode|JSON 模式/ }),
  ).toBeVisible();
  const file = expectStatus(
    await admin.get<{ content_base64: string; resource_digest: string; }>(
      `/api/mcp/servers/${server.id}/file`,
    ),
    200,
    "read MCP declaration file",
  );
  const content = Buffer.from(file.content_base64, "base64")
    .toString("utf8")
    // Endpoint policy intentionally rejects query strings. Change the path to
    // keep this a valid declaration while still producing a new file digest.
    .replace(openMcp.url, `${openMcp.url}/edited`);
  const edited = expectStatus(
    await admin.patch<McpServer>(`/api/mcp/servers/${server.id}/file`, {
      content_base64: Buffer.from(content).toString("base64"),
      expected_digest: file.resource_digest,
    }),
    200,
    "edit MCP declaration file",
  );
  expect(edited.content_digest).not.toBe(server.content_digest);
  expect(
    (
      await admin.patch(`/api/mcp/servers/${server.id}/file`, {
        content_base64: file.content_base64,
        expected_digest: file.resource_digest,
      })
    ).status,
  ).toBe(409);
});

test("OAuth controls start and disconnect a browser flow", async ({ page, admin, loginAsAdmin }) => {
  const server = await createMcpPlugin(admin, oauthMcp, {
    name: "browser-oauth",
    authType: "oauth",
    credentialMode: "per_user",
    scope: "user",
  });
  createdServers.push(server);
  const needsAuth = expectStatus(
    await admin.post<McpServer>(`/api/mcp/servers/${server.id}/probe`),
    200,
    "probe browser OAuth server",
  );
  expect(needsAuth.status).toBe("needs_auth");
  await loginAsAdmin();
  await page.goto("/settings/mcp");
  const browserOauthCard = page.getByRole("button", {
    name: "browser-oauth browser-oauth",
  });
  await expect(browserOauthCard).toBeVisible();
  await browserOauthCard.click();
  await expect(
    page.getByRole("button", { name: /Connect|连接/ }),
  ).toBeVisible();
  await page.getByRole("button", { name: /Connect|连接/ }).click();
  await page.waitForURL((url) =>
    url.pathname === "/settings/mcp"
    && url.searchParams.has("connected")
    && url.searchParams.get("connected") !== ""
  );
  const connected = expectStatus(
    await admin.get<McpServer>(`/api/mcp/servers/${server.id}`),
    200,
    "get browser-connected OAuth server",
  );
  // File-backed probes are disposable observations. OAuth completion persists
  // the grant, so the resource is credential-ready while its catalog status
  // remains unknown until the next explicit probe.
  expect(connected.status).toBe("unknown");
  expect(connected.needs_auth).toBe(false);
  const disconnected = expectStatus(
    await admin.post<McpServer>(
      `/api/mcp/servers/${server.id}/oauth/disconnect`,
    ),
    200,
    "disconnect browser OAuth",
  );
  expect(disconnected.status).toBe("unknown");
  expect(disconnected.needs_auth).toBe(true);
});
