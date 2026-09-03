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
  testIgnore: /perf\//,
  use: { browserName: "chromium", trace: "retain-on-failure", screenshot: "only-on-failure" },
});
