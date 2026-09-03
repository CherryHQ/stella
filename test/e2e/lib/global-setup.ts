import { readFile } from "node:fs/promises";
import { loadOpenAIEnv } from "./env.ts";
import { startRegistryFixture, stopRegistryFixture } from "./registry-fixture.ts";
import { startTestbed, stopTestbed } from "./testbed.ts";

export default async function globalSetup(): Promise<void> {
  loadOpenAIEnv();
  try {
    const registry = await startRegistryFixture();
    process.env.STELLA_MCP_REGISTRY_URL = registry.state.url;
    const state = await startTestbed({ fakeModel: Boolean(process.env.PERF_RUN) });
    if (process.env.PERF_RUN) {
      const credentials = JSON.parse(await readFile(state.credentialsPath, "utf8")) as { fake_model?: { base_url: string; }; };
      if (!credentials.fake_model?.base_url) throw new Error("testbed fake model credentials are missing");
      process.env.PERF_FAKE_URL = credentials.fake_model.base_url;
    }
    console.log(`[e2e] testbed ready at ${state.baseURL}`);
  } catch (error) {
    await stopTestbed();
    await stopRegistryFixture();
    throw error;
  }
}
