import { loadOpenAIEnv } from "./env.ts";
import { startRegistryFixture, stopRegistryFixture } from "./registry-fixture.ts";
import { startTestbed, stopTestbed } from "./testbed.ts";

export default async function globalSetup(): Promise<void> {
  loadOpenAIEnv();
  const registry = await startRegistryFixture();
  process.env.STELLA_MCP_REGISTRY_URL = registry.state.url;
  try {
    const state = await startTestbed();
    console.log(`[e2e] testbed ready at ${state.baseURL}`);
  } catch (error) {
    await stopTestbed();
    await stopRegistryFixture();
    throw error;
  }
}
