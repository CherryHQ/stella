import { stopTestbed } from "./testbed.ts";
import { stopRegistryFixture } from "./registry-fixture.ts";

export default async function globalTeardown(): Promise<void> {
  await stopTestbed();
  await stopRegistryFixture();
}
