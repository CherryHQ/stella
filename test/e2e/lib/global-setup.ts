import { loadOpenAIEnv } from "./env.ts";
import { startTestbed } from "./testbed.ts";

export default async function globalSetup(): Promise<void> {
  loadOpenAIEnv();
  const state = await startTestbed();
  console.log(`[e2e] testbed ready at ${state.baseURL}`);
}
