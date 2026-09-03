import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: ".",
  testMatch: /.*\.spec\.ts$/,
  globalSetup: "./lib/global-setup.ts",
  globalTeardown: "./lib/global-teardown.ts",
  workers: 1,
  fullyParallel: false,
  retries: 0,
  timeout: 180_000,
  expect: { timeout: 15_000 },
  reporter: process.env.CI ? "github" : "list",
  outputDir: "test-results",
  projects: [
    {
      name: "functional",
      testMatch: /mcp\/.*\.spec\.ts/,
      grepInvert: /@model/,
      retries: 0,
    },
    {
      name: "functional-model",
      testMatch: /mcp\/.*\.spec\.ts/,
      grep: /@model/,
      retries: 1,
    },
    {
      name: "perf",
      testMatch: /perf\/.*\.spec\.ts/,
      timeout: 300_000,
      use: { actionTimeout: 30_000 },
      retries: 0,
    },
  ],
  use: { browserName: "chromium", trace: "retain-on-failure", screenshot: "only-on-failure" },
});
