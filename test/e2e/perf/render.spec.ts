import { mkdir, writeFile } from "node:fs/promises";
import { resolve } from "node:path";
import { expect, loginWithPassword, test } from "../lib/fixtures.ts";
import { ensurePerfAgent, label, newSession, reps, seed, startFake, stopFake } from "./helpers.ts";
import { installMetrics } from "./metrics.ts";

// These tests share one seeded session so that the measured UI work is the only
// variable between repetitions. The fake provider keeps the response stable.
test.describe.configure({ mode: "serial", retries: 0 });

let agentId = "";
let sessionId = "";
const runs: Record<string, unknown>[] = [];

const turns = Number(process.env.SEED_TURNS ?? 100);

async function openSession(page: import("@playwright/test").Page, credentials: any): Promise<void> {
  await installMetrics(page);
  await page.goto(`/agents/${agentId}/sessions/${sessionId}`);
  if (page.url().includes("/login")) {
    await loginWithPassword(page, credentials.admin.email, credentials.admin.password);
    await page.goto(`/agents/${agentId}/sessions/${sessionId}`);
  }
  await expect(page.locator("body")).toContainText("cache key derived", { timeout: 60_000 });
}

async function loadFullHistory(page: import("@playwright/test").Page): Promise<void> {
  for (let attempt = 0; attempt < 80; attempt += 1) {
    await page.evaluate(() => window.__perf?.scrollTopOnce());
    const count = await page.locator("body").textContent().then((text) => (text?.match(/seed turn /g) ?? []).length);
    if (count >= turns) break;
    await page.waitForTimeout(100);
  }
  await page.evaluate(() => window.__perf?.scrollBottom());
}

test.beforeAll(async ({ admin }) => {
  startFake();
  const id = await ensurePerfAgent(admin);
  agentId = id;
  sessionId = await newSession(admin, id);
  await seed(admin, id, sessionId, turns);
});

test.afterAll(() => {
  stopFake();
});

test("long-history", async ({ page, creds }) => {
  for (let repetition = 0; repetition < reps; repetition += 1) {
    await openSession(page, creds);
    await page.waitForTimeout(500);
    await loadFullHistory(page);
    runs.push({ longHistory: await page.evaluate(() => window.__perf?.loadStats()) });
  }
});

test("streaming", async ({ page, creds }) => {
  for (let repetition = 0; repetition < reps; repetition += 1) {
    await openSession(page, creds);
    await loadFullHistory(page);
    await page.evaluate(() => window.__perf?.start());

    const nonce = `n${Date.now()}`;
    await page.locator("textarea").fill(`stream ${nonce}`);
    await page.locator("textarea").press("Enter");
    await expect(page.locator("body")).toContainText(`END-OF-STREAM ${nonce}`, { timeout: 180_000 });
    runs[repetition].streaming = await page.evaluate(() => window.__perf?.stop());
  }
});

test("typing", async ({ page, creds }) => {
  for (let repetition = 0; repetition < reps; repetition += 1) {
    await openSession(page, creds);
    await loadFullHistory(page);
    runs[repetition].typing = await page.evaluate(() => window.__perf?.typeInto("textarea", "the quick brown fox ".repeat(6)));
    await page.evaluate(() => window.__perf?.clearComposer("textarea"));
  }

  await mkdir(resolve("perf", "results"), { recursive: true });
  await writeFile(
    resolve("perf", "results", `${label}.json`),
    JSON.stringify({ label, date: new Date().toISOString(), commit: process.env.GIT_COMMIT ?? "unknown", reps, runs }, null, 2),
  );
});
