// Sibling-replica lifecycle: a second stellad attached to the testbed's
// PostgreSQL and vault, so specs can exercise cross-replica session streaming.
import { type ChildProcess, spawn } from "node:child_process";
import { closeSync, mkdtempSync, openSync, writeFileSync } from "node:fs";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { resolve } from "node:path";
import { e2eDir, repoRoot } from "./env.ts";
import type { TestbedCredentials } from "./testbed.ts";

export interface SiblingReplica {
  baseURL: string;
  pid: number;
  stop(): Promise<void>;
}

const stelladBinary = resolve(repoRoot, "dist", "bin", "stellad");

function freePort(): Promise<number> {
  return new Promise((resolvePort, reject) => {
    const srv = createServer();
    srv.once("error", reject);
    srv.listen(0, "127.0.0.1", () => {
      const address = srv.address();
      if (!address || typeof address === "string") {
        srv.close();
        reject(new Error("no ephemeral port"));
        return;
      }
      srv.close(() => resolvePort(address.port));
    });
  });
}

// Spawns one sibling stellad on the testbed's database and vault key. The
// sibling gets its own STELLA_HOME — only the database is shared, which is
// exactly the multi-replica contract under test.
export async function startSiblingReplica(creds: TestbedCredentials): Promise<SiblingReplica> {
  if (!creds.database_url || !creds.vault_key) {
    throw new Error("credentials lack database_url/vault_key — sibling replica cannot attach");
  }
  const port = await freePort();
  const baseURL = `http://127.0.0.1:${port}`;
  const home = mkdtempSync(resolve(tmpdir(), "stella-replica-"));
  const logPath = resolve(e2eDir, "test-results", `replica-${port}.log`);
  const logFd = openSync(logPath, "a");
  const child: ChildProcess = spawn(stelladBinary, ["server"], {
    cwd: repoRoot,
    env: {
      PATH: process.env.PATH ?? "",
      HOME: process.env.HOME ?? "",
      TMPDIR: process.env.TMPDIR ?? "",
      STELLA_HOME: home,
      STELLA_DATABASE_URL: creds.database_url,
      STELLA_VAULT_KEY: creds.vault_key,
      STELLA_SERVER_URL: baseURL,
      STELLA_MCP_ALLOW_PRIVATE_ENDPOINTS: "1",
      // The sibling never claims durable runs: every turn executes on the
      // primary, so anything this replica serves crossed the database.
      STELLA_RUN_WORKER: "off",
      HOST: "127.0.0.1",
      PORT: String(port),
    },
    stdio: ["ignore", logFd, logFd],
  });
  closeSync(logFd);

  const deadline = Date.now() + 60_000;
  let exited = false;
  child.on("exit", () => {
    exited = true;
  });
  for (;;) {
    if (exited) throw new Error(`sibling replica exited before ready; see ${logPath}`);
    try {
      const res = await fetch(`${baseURL}/healthz`);
      if (res.ok) break;
    } catch {
      // not up yet
    }
    if (Date.now() > deadline) {
      child.kill("SIGKILL");
      throw new Error(`sibling replica did not become ready in 60s; see ${logPath}`);
    }
    await new Promise((r) => setTimeout(r, 200));
  }
  const pid = child.pid ?? 0;
  return {
    baseURL,
    pid,
    stop: () =>
      new Promise<void>((resolveStop) => {
        if (exited) {
          resolveStop();
          return;
        }
        child.once("exit", () => resolveStop());
        child.kill("SIGTERM");
        setTimeout(() => {
          if (!exited) child.kill("SIGKILL");
        }, 15_000).unref();
      }),
  };
}
