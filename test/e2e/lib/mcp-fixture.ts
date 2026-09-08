import { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { StreamableHTTPServerTransport } from "@modelcontextprotocol/sdk/server/streamableHttp.js";
import { createHash } from "node:crypto";
import { type IncomingMessage, type ServerResponse } from "node:http";
import { z } from "zod";
import { type ApiClient, expectStatus } from "./api.ts";
import { startFixtureServer } from "./fixture-server.ts";
import { type McpDeclaration, type McpServer as McpServerResource, type PluginResource } from "./types.ts";

export interface McpFixtureOptions {
  // Requests must carry `Authorization: Bearer <bearer>`; anything else is 401.
  bearer?: string;
  // OAuth-protected mode: advertise the AS through RFC 9728 metadata and
  // accept only tokens currently approved by the fixture.
  protectedResourceMetadata?: string;
  bearerValidator?: (token: string) => boolean;
  // Extra tool names to advertise, each echoing its arguments.
  extraTools?: string[];
}

export interface RecordedCall {
  tool: string;
  args: Record<string, unknown>;
}

export interface McpFixture {
  url: string;
  port: number;
  calls: RecordedCall[];
  // JSON-RPC method counts (initialize, tools/list, tools/call, ...), so a spec
  // can prove sessions are shared rather than reopened per call.
  methods: Map<string, number>;
  close(): Promise<void>;
}

export interface CreateMcpPluginOptions {
  name?: string;
  scope?: "system" | "system_agent" | "user" | "user_agent";
  agentId?: string;
  enabled?: boolean;
  authType?: "none" | "bearer" | "oauth";
  credentialMode?: "shared" | "per_user";
  credentialRef?: string;
  token?: string;
  metadata?: Record<string, unknown>;
  url?: string;
  description?: string;
}

// Keep this in lockstep with agentpackage.ExportedToolName. The e2e suite
// speaks to the model-facing API, so it must assert the exported identity
// rather than the retired package__local spelling.
export function exportedMcpName(
  packageName: string,
  localToolName: string,
): string {
  const digest = createHash("sha256")
    .update(JSON.stringify([packageName, "main", localToolName]))
    .digest("hex")
    .slice(0, 12);
  const sanitize = (value: string, fallback: string): string => {
    const segment = Array.from(value)
      .map((char) => (/[A-Za-z0-9_-]/.test(char) ? char : "_"))
      .join("")
      .replace(/^[_-]+|[_-]+$/g, "");
    return segment || fallback;
  };
  let prefix = [
    sanitize(packageName, "package"),
    sanitize("main", "server"),
    sanitize(localToolName, "tool"),
  ].join("_");
  if (prefix.length > 51) prefix = prefix.slice(0, 51).replace(/[_-]+$/g, "");
  if (!prefix) prefix = "mcp";
  return `${prefix}_${digest}`;
}

// Keep setup on the file-backed MCP API so browser/API tests exercise the same
// resource IDs, content digests, and CAS preconditions as production callers.
export async function createMcpPlugin(
  api: ApiClient,
  fixture: Pick<McpFixture, "url">,
  options: CreateMcpPluginOptions = {},
): Promise<McpServerResource> {
  const name = options.name ?? "e2e";
  const declaration: McpDeclaration = {
    url: options.url ?? fixture.url,
    transport: "streamable_http",
    auth_type: options.authType ?? "none",
    credential_mode: options.credentialMode ?? "per_user",
    ...(options.credentialRef ? { credential_ref: options.credentialRef } : {}),
    ...(options.description ? { description: options.description } : {}),
  };
  const created = expectStatus(
    await api.post<McpServerResource>("/api/mcp/servers", {
      name,
      scope: options.scope ?? "user",
      ...(options.agentId ? { agent_id: options.agentId } : {}),
      declaration,
    }),
    201,
    "create MCP server",
  );
  if (options.enabled === false) {
    return expectStatus(
      await api.patch<McpServerResource>(`/api/mcp/servers/${created.id}`, {
        is_enabled: false,
        expected_settings_digest: created.settings_digest,
      }),
      200,
      "disable MCP server",
    );
  }
  if (options.token !== undefined) {
    return expectStatus(
      await api.patch<McpServerResource>(
        `/api/mcp/servers/${created.id}/credentials`,
        {
          expected_digest: created.content_digest,
          bearer_token: options.token,
        },
      ),
      200,
      "store MCP bearer token",
    );
  }
  return created;
}

export function pluginPath(resource: PluginResource | string): string {
  const id = typeof resource === "string" ? resource : resource.id;
  return `/api/plugins/${encodeURIComponent(id)}`;
}

export async function pluginResource(
  api: ApiClient,
  id: string,
): Promise<PluginResource> {
  return expectStatus(
    await api.get<PluginResource>(pluginPath(id)),
    200,
    `get plugin resource ${id}`,
  );
}

export async function mcpServers(
  api: ApiClient,
  query = "",
): Promise<McpServerResource[]> {
  return expectStatus(
    await api.get<{ servers: McpServerResource[]; }>(
      `/api/mcp/servers${query ? `?${query}` : ""}`,
    ),
    200,
    "list MCP servers",
  ).servers;
}

export async function mcpServer(
  api: ApiClient,
  id: string,
): Promise<McpServerResource> {
  return expectStatus(
    await api.get<McpServerResource>(`/api/mcp/servers/${id}`),
    200,
    `get MCP server ${id}`,
  );
}

export async function deleteMcpServer(
  api: ApiClient,
  server: Pick<McpServerResource, "id" | "content_digest">,
): Promise<void> {
  const current = await api.get<McpServerResource>(
    `/api/mcp/servers/${server.id}`,
  );
  const digest = current.status === 200
    ? current.body.content_digest
    : server.content_digest;
  const response = await api.delete(
    `/api/mcp/servers/${server.id}?expected_digest=${encodeURIComponent(digest)}`,
  );
  if (response.status !== 204 && response.status !== 404) {
    throw new Error(
      `delete MCP server: want HTTP 204 or 404, got ${response.status}: ${JSON.stringify(response.body)}`,
    );
  }
}

function buildServer(
  options: McpFixtureOptions,
  fixture: McpFixture,
): McpServer {
  const server = new McpServer({
    name: "stella-e2e-fixture",
    version: "1.0.0",
  });
  server.registerTool(
    "add",
    {
      description: "Add two integers and return their sum.",
      inputSchema: {
        a: z.number().describe("first addend"),
        b: z.number().describe("second addend"),
      },
    },
    async ({ a, b }) => {
      fixture.calls.push({ tool: "add", args: { a, b } });
      return { content: [{ type: "text", text: String(a + b) }] };
    },
  );
  server.registerTool(
    "echo",
    {
      description: "Echo the given text back verbatim.",
      inputSchema: { text: z.string() },
    },
    async ({ text }) => {
      fixture.calls.push({ tool: "echo", args: { text } });
      return { content: [{ type: "text", text }] };
    },
  );
  for (const name of options.extraTools ?? []) {
    server.registerTool(
      name,
      { description: `Fixture tool ${name}.`, inputSchema: {} },
      async (args) => {
        fixture.calls.push({
          tool: name,
          args: (args ?? {}) as Record<string, unknown>,
        });
        return { content: [{ type: "text", text: `${name} ok` }] };
      },
    );
  }
  return server;
}

async function readJSON(req: IncomingMessage): Promise<unknown> {
  const chunks: Buffer[] = [];
  for await (const chunk of req) chunks.push(chunk as Buffer);
  const raw = Buffer.concat(chunks).toString("utf8");
  return raw ? JSON.parse(raw) : undefined;
}

// A stateless Streamable HTTP MCP server on a random loopback port. Stateless
// means every POST is served by a fresh transport, which is exactly what the
// Go SDK client expects from a server that does not hand out session ids.
export async function startMcpFixture(
  options: McpFixtureOptions = {},
): Promise<McpFixture> {
  const fixture: McpFixture = {
    url: "",
    port: 0,
    calls: [],
    methods: new Map(),
    close: async () => {},
  };
  const fixtureServer = await startFixtureServer(
    async (req: IncomingMessage, res: ServerResponse) => {
      try {
        const authorization = req.headers.authorization ?? "";
        const token = authorization.startsWith("Bearer ")
          ? authorization.slice("Bearer ".length)
          : "";
        if (
          (options.bearer && authorization !== `Bearer ${options.bearer}`)
          || (options.bearerValidator && !options.bearerValidator(token))
        ) {
          const challenge = options.protectedResourceMetadata
            ? `Bearer error="invalid_token", resource_metadata="${options.protectedResourceMetadata}"`
            : "Bearer";
          res.writeHead(401, {
            "Content-Type": "application/json",
            "WWW-Authenticate": challenge,
          });
          res.end(JSON.stringify({ error: "unauthorized" }));
          return;
        }
        if (req.method !== "POST") {
          res.writeHead(405, { Allow: "POST" });
          res.end();
          return;
        }
        const body = await readJSON(req);
        for (const msg of Array.isArray(body) ? body : [body]) {
          const method = (msg as { method?: string; })?.method;
          if (method) {
            fixture.methods.set(method, (fixture.methods.get(method) ?? 0) + 1);
          }
        }
        const transport = new StreamableHTTPServerTransport({
          sessionIdGenerator: undefined,
        });
        const server = buildServer(options, fixture);
        res.on("close", () => {
          void transport.close();
          void server.close();
        });
        await server.connect(transport);
        await transport.handleRequest(req, res, body);
      } catch (err) {
        if (!res.headersSent) {
          res.writeHead(500, { "Content-Type": "application/json" });
          res.end(JSON.stringify({ error: String(err) }));
        }
      }
    },
  );
  fixture.port = Number(new URL(fixtureServer.state.url).port);
  fixture.url = `${fixtureServer.state.url}/mcp`;
  fixture.close = fixtureServer.close;
  return fixture;
}
