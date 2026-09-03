import { type ChildProcess, spawn, spawnSync } from "node:child_process";
import { closeSync, existsSync, mkdirSync, openSync, readFileSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";
import { e2eDir, repoRoot } from "./env.ts";

export interface TestbedCredentials {
  version: number;
  base_url: string;
  database_url?: string;
  admin: { id: string; email: string; role: string; password: string; token: string; };
  user: { id: string; email: string; role: string; token: string; };
  fake_model?: { provider_id: string; base_url: string; };
}

export interface TestbedState {
  baseURL: string;
  credentialsPath: string;
}

const stateFile = resolve(e2eDir, "test-results", "testbed.json");
const testbedBinary = resolve(repoRoot, "dist", "bin", "testbed");
const stelladBinary = resolve(repoRoot, "dist", "bin", "stellad");

export function testbedPort(): number {
  const raw = process.env.STELLA_E2E_PORT ?? "25777";
  const port = Number.parseInt(raw, 10);
  if (!Number.isInteger(port) || port < 1 || port > 65535) {
    throw new Error(`STELLA_E2E_PORT=${raw} is not a valid port`);
  }
  return port;
}

// Starts the disposable testbed (embedded PostgreSQL + stellad + fixture
// accounts) and resolves once its credentials file is announced. The process
// keeps running until `testbed stop` is called from the same checkout.
export async function startTestbed(options: { fakeModel?: boolean; } = {}): Promise<TestbedState> {
  if (!existsSync(stelladBinary)) {
    throw new Error(`${stelladBinary} is missing: run \`mise run build\` first`);
  }
  const build = spawnSync("go", ["build", "-o", testbedBinary, "./test/testbed/cmd"], {
    cwd: repoRoot,
    stdio: "inherit",
  });
  if (build.status !== 0) throw new Error("go build ./test/testbed failed");

  const port = testbedPort();
  mkdirSync(resolve(e2eDir, "test-results"), { recursive: true });
  // stdout goes to a log file, never a pipe: a Go process dies on SIGPIPE when
  // its stdout reader disappears, which would skip the testbed's own cleanup.
  const logPath = resolve(e2eDir, "test-results", "testbed.log");
  writeFileSync(logPath, "");
  const logFd = openSync(logPath, "a");
  const args = ["start", "-port", String(port)];
  if (options.fakeModel) {
    args.push("-fake-model");
    // Perf runs pace the fake model through the documented PERF_STREAM_* knobs.
    if (process.env.PERF_STREAM_CHUNKS) args.push("-fake-stream-chunks", process.env.PERF_STREAM_CHUNKS);
    if (process.env.PERF_STREAM_INTERVAL_MS) args.push("-fake-stream-interval-ms", process.env.PERF_STREAM_INTERVAL_MS);
  }
  const child: ChildProcess = spawn(testbedBinary, args, {
    cwd: repoRoot,
    // Local MCP fixtures listen on loopback, which the production policy refuses.
    env: { ...process.env, STELLA_MCP_ALLOW_PRIVATE_ENDPOINTS: "1" },
    stdio: ["ignore", logFd, logFd],
    detached: true,
  });
  child.unref();
  closeSync(logFd);

  const deadline = Date.now() + 180_000;
  let exited: number | null = null;
  child.on("exit", (code) => {
    exited = code ?? -1;
  });
  for (;;) {
    const out = readFileSync(logPath, "utf8");
    const base = /Stella testbed: (\S+)/.exec(out);
    const creds = /Credentials: (\S+)/.exec(out);
    if (base && creds) {
      const state = { baseURL: base[1], credentialsPath: creds[1] };
      writeFileSync(stateFile, JSON.stringify(state, null, 2));
      return state;
    }
    if (exited !== null) throw new Error(`testbed exited with code ${exited} before ready; see ${logPath}`);
    if (Date.now() > deadline) throw new Error(`testbed did not become ready in 3 minutes; see ${logPath}`);
    await new Promise((r) => setTimeout(r, 250));
  }
}

// Async on purpose: `testbed stop` polls until the supervisor PID is gone, and
// the supervisor is our child, so the event loop must stay free to reap it.
// A blocking spawnSync would leave it a zombie and the stop would time out.
export function stopTestbed(): Promise<void> {
  if (!existsSync(testbedBinary)) return Promise.resolve();
  return new Promise((resolvePromise) => {
    const child = spawn(testbedBinary, ["stop"], { cwd: repoRoot, stdio: "inherit" });
    child.on("exit", () => resolvePromise());
  });
}

export function loadTestbedState(): TestbedState {
  if (!existsSync(stateFile)) {
    throw new Error(`${stateFile} is missing: the Playwright global setup did not start a testbed`);
  }
  return JSON.parse(readFileSync(stateFile, "utf8")) as TestbedState;
}

export function loadCredentials(state: TestbedState): TestbedCredentials {
  return JSON.parse(readFileSync(state.credentialsPath, "utf8")) as TestbedCredentials;
}
