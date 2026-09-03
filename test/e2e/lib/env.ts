import { readFileSync } from "node:fs";
import { resolve } from "node:path";

export const e2eDir = resolve(import.meta.dirname, "..");
export const repoRoot = resolve(e2eDir, "../..");

// Playwright workers run under node, so bun's automatic .env loading does not
// apply. Only the OPENAI_* keys are read from the repo root .env, and a value
// already in the environment always wins.
export function loadOpenAIEnv(): void {
  let text: string;
  try {
    text = readFileSync(resolve(repoRoot, ".env"), "utf8");
  } catch {
    return;
  }
  for (const raw of text.split("\n")) {
    const line = raw.trim();
    if (!line || line.startsWith("#")) continue;
    const eq = line.indexOf("=");
    if (eq < 0) continue;
    const key = line.slice(0, eq).trim();
    if (!key.startsWith("OPENAI_") || process.env[key]) continue;
    let value = line.slice(eq + 1).trim();
    if ((value.startsWith('"') && value.endsWith('"')) || (value.startsWith("'") && value.endsWith("'"))) {
      value = value.slice(1, -1);
    }
    process.env[key] = value;
  }
}

export interface OpenAIEnv {
  apiKey: string;
  baseURL: string;
  model: string;
}

export function openAIEnv(): OpenAIEnv {
  loadOpenAIEnv();
  const apiKey = process.env.OPENAI_API_KEY ?? "";
  const baseURL = process.env.OPENAI_BASE_URL ?? "https://api.openai.com/v1";
  const model = process.env.OPENAI_MODEL ?? "";
  if (!apiKey || !model) {
    throw new Error("OPENAI_API_KEY and OPENAI_MODEL are required (set them in the repo root .env)");
  }
  return { apiKey, baseURL, model };
}
