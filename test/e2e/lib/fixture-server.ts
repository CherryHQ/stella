import { mkdirSync, writeFileSync } from "node:fs";
import { createServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";
import { resolve } from "node:path";
import { e2eDir } from "./env.ts";

export interface FixtureServerState {
  url: string;
  statePath?: string;
}
export interface FixtureServer {
  server: Server;
  state: FixtureServerState;
  close(): Promise<void>;
}

// Shared listen/close/state-file lifecycle for local OAuth, MCP, and registry fixtures.
export async function startFixtureServer(
  handler: (req: IncomingMessage, res: ServerResponse) => void | Promise<unknown>,
  stateName?: string,
): Promise<FixtureServer> {
  const server = createServer(handler);
  await new Promise<void>((resolveListen) => server.listen(0, "127.0.0.1", resolveListen));
  const address = server.address();
  if (!address || typeof address === "string") throw new Error("fixture server did not bind");
  const state: FixtureServerState = { url: `http://127.0.0.1:${address.port}` };
  if (stateName) {
    const statePath = resolve(e2eDir, "test-results", stateName);
    mkdirSync(resolve(e2eDir, "test-results"), { recursive: true });
    writeFileSync(statePath, JSON.stringify(state));
    state.statePath = statePath;
  }
  return {
    server,
    state,
    close: () => new Promise<void>((resolveClose, reject) => server.close((error) => error ? reject(error) : resolveClose())),
  };
}
