import path from "node:path";

import { defineConfig, devices } from "@playwright/test";

function requiredAbsoluteEnv(name: string): string {
  const value = process.env[name];
  if (!value) {
    throw new Error(`${name} is required`);
  }
  if (!path.isAbsolute(value)) {
    throw new Error(`${name} must be an absolute path`);
  }
  return value;
}

const baseURL = process.env.STELLA_E2E_BASE_URL;
if (!baseURL) {
  throw new Error("STELLA_E2E_BASE_URL is required");
}

export default defineConfig({
  testDir: "./e2e",
  outputDir: requiredAbsoluteEnv("STELLA_E2E_OUTPUT_DIR"),
  fullyParallel: false,
  workers: 1,
  retries: 0,
  timeout: 90_000,
  expect: {
    timeout: 15_000,
  },
  reporter: [["line"], ["./e2e/release-reporter.ts"]],
  use: {
    ...devices["Desktop Chrome"],
    baseURL,
    // Fail stale UI locators promptly while retaining a larger budget for
    // end-to-end operations such as streamed replies and anonymous share loads.
    actionTimeout: 15_000,
    navigationTimeout: 30_000,
    locale: "en-US",
    viewport: { width: 1440, height: 900 },
    permissions: ["clipboard-read", "clipboard-write"],
    screenshot: "only-on-failure",
    trace: "retain-on-failure",
    video: "off",
  },
});
