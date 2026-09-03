import { stopTestbed } from "./testbed.ts";

export default async function globalTeardown(): Promise<void> {
  await stopTestbed();
}
