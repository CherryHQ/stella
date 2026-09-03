import { createServer, type Server } from "node:http";
import { mkdirSync, writeFileSync, readFileSync } from "node:fs";
import { resolve } from "node:path";
import { startMcpFixture, type McpFixture } from "./mcp-fixture.ts";
import { e2eDir } from "./env.ts";

export interface RegistryFixtureState { url: string; mcpUrl: string }

const statePath = resolve(e2eDir, "test-results", "registry.json");
let active: (() => Promise<void>) | undefined;

function entry(name: string, version: string, url: string, headers: unknown[] = [], latest = true) {
  return { server: { name, description: name, version, remotes: [{ type: "streamable-http", url, headers }] }, _meta: { "io.modelcontextprotocol.registry/official": { isLatest: latest } } };
}

export async function startRegistryFixture(): Promise<{ state: RegistryFixtureState; close: () => Promise<void> }> {
  const mcp = await startMcpFixture();
  const pages = {
    first: [
      entry("com.stella/registry-add", "1.0.0", mcp.url),
      entry("com.stella/bearer", "1.0.0", "http://127.0.0.1:1/bearer", [{ name: "Authorization", value: "Bearer {api_key}", isRequired: true, isSecret: true }]),
      { server: { name: "com.stella/sse-only", version: "1.0.0", remotes: [{ type: "sse", url: "http://sse-only" }] } },
    ],
    second: [
      entry("com.stella/unsupported", "2.0.0", "http://127.0.0.1:1/unsupported", [{ name: "X-Tenant", value: "{tenant}", isRequired: true }]),
      { server: { name: "com.stella/stdio-only", version: "1.0.0", packages: [{ transport: { type: "stdio" } }] } },
    ],
  };
  const server: Server = createServer((req, res) => {
    res.setHeader("Content-Type", "application/json");
    if (req.url?.startsWith("/__e2e/mcp-calls")) {
      res.end(JSON.stringify({ calls: mcp.calls, methods: Object.fromEntries(mcp.methods) }));
      return;
    }
    if (req.url?.startsWith("/v0/servers/") && req.url.endsWith("/versions")) {
      const id = decodeURIComponent(req.url.slice("/v0/servers/".length, -"/versions".length));
      if (id === "com.stella/unknown") { res.writeHead(404); res.end("{}"); return; }
      if (id === "com.stella/registry-add") {
        res.end(JSON.stringify({ servers: [entry(id, "0.9.0", "http://127.0.0.1:1/old", [], false), entry(id, "1.0.0", mcp.url)] }));
        return;
      }
      res.writeHead(404); res.end("{}"); return;
    }
    if (req.url?.startsWith("/v0/servers")) {
      const query = new URL(req.url, "http://127.0.0.1");
      if (query.searchParams.get("search") === "rate-limit") { res.writeHead(429, { "Retry-After": "17" }); res.end("{}"); return; }
      if (query.searchParams.get("search") === "upstream-error") { res.writeHead(500); res.end("{}"); return; }
      const second = query.searchParams.get("cursor") === "page-2";
      res.end(JSON.stringify({ servers: second ? pages.second : pages.first, metadata: { nextCursor: second ? "" : "page-2" } }));
      return;
    }
    res.writeHead(404); res.end("{}");
  });
  await new Promise<void>((resolveListen) => server.listen(0, "127.0.0.1", resolveListen));
  const address = server.address();
  if (!address || typeof address === "string") throw new Error("registry fixture did not bind");
  const state = { url: `http://127.0.0.1:${address.port}`, mcpUrl: mcp.url };
  mkdirSync(resolve(e2eDir, "test-results"), { recursive: true });
  writeFileSync(statePath, JSON.stringify(state));
  const close = async () => { await mcp.close(); await new Promise<void>((resolveClose, reject) => server.close((err) => err ? reject(err) : resolveClose())); };
  active = close;
  return { state, close };
}

export async function stopRegistryFixture(): Promise<void> {
  await active?.();
  active = undefined;
}

export function loadRegistryFixtureState(): RegistryFixtureState {
  return JSON.parse(readFileSync(statePath, "utf8")) as RegistryFixtureState;
}
