import { spawnSync } from "node:child_process";
import { stopFake } from "../perf/helpers.ts";
import { stopRegistryFixture } from "./registry-fixture.ts";
import { stopTestbed } from "./testbed.ts";

function killLeftoverServers(): void {
  const pattern = "dist/bin/stellad serve";
  spawnSync("pkill", ["-TERM", "-f", pattern]);
  spawnSync("pkill", ["-KILL", "-f", pattern]);
}

export default async function globalTeardown(): Promise<void> {
  stopFake();
  try {
    await stopTestbed();
  } finally {
    await stopRegistryFixture();
    killLeftoverServers();
  }
}
