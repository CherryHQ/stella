import { spawnSync } from "node:child_process";
import { stopRegistryFixture } from "./registry-fixture.ts";
import { stopTestbed, testbedPort } from "./testbed.ts";

// `testbed stop` only signals a live supervisor. A supervisor that died
// abruptly leaves its stellad running as its own process-group leader, so find
// that stellad by the port it owns. A name pattern would also hit a developer's
// `mise run dev` server, which runs the same binary from this checkout.
function killLeftoverServer(port: number): void {
  const found = spawnSync("lsof", ["-ti", `tcp:${port}`, "-sTCP:LISTEN"], { encoding: "utf8" });
  for (const pid of (found.stdout ?? "").split("\n").filter(Boolean)) {
    try {
      process.kill(Number(pid), "SIGTERM");
    } catch {
      // Already gone.
    }
  }
}

export default async function globalTeardown(): Promise<void> {
  try {
    await stopTestbed();
  } finally {
    await stopRegistryFixture();
    killLeftoverServer(testbedPort());
  }
}
