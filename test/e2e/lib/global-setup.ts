import { loadOpenAIEnv } from "./env.ts";
import { startRegistryFixture } from "./registry-fixture.ts";
import { startTestbed } from "./testbed.ts";

export default async function globalSetup(): Promise<void> {
  loadOpenAIEnv();
  const registry = await startRegistryFixture();
  process.env.STELLA_MCP_REGISTRY_URL = registry.state.url;
  const state = await startTestbed();
  console.log(`[e2e] testbed ready at ${state.baseURL}`);
}
