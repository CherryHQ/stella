// PR #1236: official MCP Registry search, install provenance, and agent use.
import { createChatSession, ensureAgent, invokedToolNames, sendTurn, sessionMessages } from "../lib/agent.ts";
import { expectStatus } from "../lib/api.ts";
import { expect, test } from "../lib/fixtures.ts";
import { ensureProvider } from "../lib/provider.ts";
import { loadRegistryFixtureState } from "../lib/registry-fixture.ts";
import { McpServer, RegistryServer } from "../lib/types.ts";

test.describe.configure({ mode: "serial" });

const state = loadRegistryFixtureState();
let installed: McpServer;

async function registryCalls(baseURL: string) {
  const response = await fetch(`${baseURL}/__e2e/mcp-calls`);
  return await response.json() as { calls: { tool: string; args: Record<string, unknown>; }[]; };
}

test("registry API filters transports, classifies auth, and resumes at an upstream page boundary", async ({ admin }) => {
  const first = expectStatus(
    await admin.get<{ servers: RegistryServer[]; next_page_token?: string; }>("/api/mcp/registry/servers?q=anything&page_size=2"),
    200,
    "registry search",
  );
  expect(first.servers.map((s) => s.auth)).toEqual(["none", "bearer"]);
  expect(first.servers.every((s) => s.transport === "streamable_http")).toBe(true);
  expect(first.servers.some((s) => s.id.includes("sse-only"))).toBe(false);
  expect(first.next_page_token).toBeTruthy();
  const second = expectStatus(
    await admin.get<{ servers: RegistryServer[]; next_page_token?: string; }>(
      `/api/mcp/registry/servers?page_size=2&page_token=${encodeURIComponent(first.next_page_token!)}`,
    ),
    200,
    "registry next page",
  );
  expect(second.servers.map((s) => s.auth)).toEqual(["unsupported"]);
  expect(second.servers[0].headers?.[0].template).toContain("{tenant}");
  expect(second.next_page_token).toBeFalsy();
});

test("registry detail prefers latest, unknown ids 404, and upstream failures map cleanly", async ({ admin }) => {
  const detail = expectStatus(
    await admin.get<RegistryServer>(`/api/mcp/registry/servers/official/${encodeURIComponent("com.stella/registry-add")}`),
    200,
    "registry detail",
  );
  expect(detail.version).toBe("1.0.0");
  expect(detail.url).toBe(state.mcpUrl);
  expect((await admin.get(`/api/mcp/registry/servers/official/${encodeURIComponent("com.stella/unknown")}`)).status).toBe(404);
  const limited = await admin.get("/api/mcp/registry/servers?q=rate-limit");
  expect(limited.status).toBe(503);
  expect(limited.headers.get("retry-after")).toBe("17");
  expect((await admin.get("/api/mcp/registry/servers?q=upstream-error")).status).toBe(502);
});

test("install persists provenance, probes the catalog, and rejects a same-scope URL twin", async ({ admin, db }) => {
  installed = expectStatus(
    await admin.post<McpServer>("/api/mcp/servers", {
      scope: "user",
      name: "registry-add",
      url: state.mcpUrl,
      transport: "streamable_http",
      auth_type: "none",
      source: "official",
      source_id: "com.stella/registry-add",
      source_version: "1.0.0",
    }),
    201,
    "install registry server",
  );
  expect(installed.status).toBe("ok");
  expect(installed.tools?.map((tool) => tool.name).sort()).toEqual(["add", "echo"]);
  const row = (await db`select metadata, status, tools from mcp_server where id = ${installed.id}`)[0];
  expect(row.status).toBe("ok");
  expect((row.tools as { name: string; }[]).map((tool) => tool.name).sort()).toEqual(["add", "echo"]);
  expect(row.metadata).toMatchObject({ registry: { source: "official", id: "com.stella/registry-add", version: "1.0.0" } });
  const twin = await admin.post("/api/mcp/servers", { scope: "user", name: "registry-twin", url: state.mcpUrl });
  expect(twin.status).toBe(409);
  expect(JSON.stringify(twin.body)).toContain(installed.id);
  expect((await admin.delete(`/api/mcp/servers/${installed.id}`)).status).toBe(204);
});

test("a real agent calls add on the registry-installed server @model", async ({ admin }) => {
  test.setTimeout(300_000);
  // Install through the API with provenance; the browser install path is covered by the #1237 spec.
  installed = expectStatus(
    await admin.post<McpServer>("/api/mcp/servers", {
      scope: "user",
      name: "registry-add",
      url: state.mcpUrl,
      transport: "streamable_http",
      auth_type: "none",
      source: "official",
      source_id: "com.stella/registry-add",
      source_version: "1.0.0",
    }),
    201,
    "install registry server for the agent turn",
  );
  expect(installed.status).toBe("ok");
  const { modelRef } = await ensureProvider(admin);
  const agentId = await ensureAgent(admin, modelRef, "e2e-registry-agent");
  const sessionId = await createChatSession(admin, agentId);
  const before = (await registryCalls(state.url)).calls.length;
  const turn = await sendTurn(admin, agentId, sessionId, "Call mcp__registry_add__add with a=17 and b=25. Reply with only the result.");
  expect(turn.errors, JSON.stringify(turn.events.slice(-5))).toEqual([]);
  expect(turn.text).toContain("42");
  const calls = (await registryCalls(state.url)).calls.slice(before);
  expect(calls.some((call) => call.tool === "add" && call.args.a === 17 && call.args.b === 25)).toBe(true);
  expect(invokedToolNames(await sessionMessages(admin, agentId, sessionId))).toContain("mcp__registry_add__add");
  expect((await admin.delete(`/api/mcp/servers/${installed.id}`)).status).toBe(204);
});

test("live registry search has the expected response shape", async () => {
  test.skip(!process.env.STELLA_E2E_LIVE_REGISTRY, "set STELLA_E2E_LIVE_REGISTRY=1 to run the external smoke check");
  const response = await fetch("https://registry.modelcontextprotocol.io/v0/servers?search=notion&limit=5");
  expect(response.ok).toBe(true);
  const body = await response.json() as { servers?: unknown[]; metadata?: { nextCursor?: unknown; }; };
  expect(Array.isArray(body.servers)).toBe(true);
  expect(body.metadata === undefined || typeof body.metadata.nextCursor === "string" || body.metadata.nextCursor === null).toBe(true);
});
