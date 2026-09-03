import { createServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";
import { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { StreamableHTTPServerTransport } from "@modelcontextprotocol/sdk/server/streamableHttp.js";
import { z } from "zod";

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

function buildServer(options: McpFixtureOptions, fixture: McpFixture): McpServer {
  const server = new McpServer({ name: "stella-e2e-fixture", version: "1.0.0" });
  server.registerTool(
    "add",
    {
      description: "Add two integers and return their sum.",
      inputSchema: { a: z.number().describe("first addend"), b: z.number().describe("second addend") },
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
    server.registerTool(name, { description: `Fixture tool ${name}.`, inputSchema: {} }, async (args) => {
      fixture.calls.push({ tool: name, args: (args ?? {}) as Record<string, unknown> });
      return { content: [{ type: "text", text: `${name} ok` }] };
    });
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
export async function startMcpFixture(options: McpFixtureOptions = {}): Promise<McpFixture> {
  const fixture: McpFixture = {
    url: "",
    port: 0,
    calls: [],
    methods: new Map(),
    close: async () => {},
  };
  const httpServer: Server = createServer(async (req: IncomingMessage, res: ServerResponse) => {
    try {
      const authorization = req.headers.authorization ?? "";
      const token = authorization.startsWith("Bearer ") ? authorization.slice("Bearer ".length) : "";
      if (
        (options.bearer && authorization !== `Bearer ${options.bearer}`) ||
        (options.bearerValidator && !options.bearerValidator(token))
      ) {
        const challenge = options.protectedResourceMetadata
          ? `Bearer error="invalid_token", resource_metadata="${options.protectedResourceMetadata}"`
          : "Bearer";
        res.writeHead(401, { "Content-Type": "application/json", "WWW-Authenticate": challenge });
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
        const method = (msg as { method?: string })?.method;
        if (method) fixture.methods.set(method, (fixture.methods.get(method) ?? 0) + 1);
      }
      const transport = new StreamableHTTPServerTransport({ sessionIdGenerator: undefined });
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
  });
  await new Promise<void>((resolve) => httpServer.listen(0, "127.0.0.1", resolve));
  const address = httpServer.address();
  if (!address || typeof address === "string") throw new Error("fixture did not bind a TCP port");
  fixture.port = address.port;
  fixture.url = `http://127.0.0.1:${address.port}/mcp`;
  fixture.close = () => new Promise<void>((resolve, reject) => httpServer.close((err) => (err ? reject(err) : resolve())));
  return fixture;
}
