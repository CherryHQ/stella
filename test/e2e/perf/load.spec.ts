import { mkdir, writeFile } from "node:fs/promises";
import { resolve } from "node:path";
import { expect, loginWithPassword, test } from "../lib/fixtures.ts";
import {
  ensurePerfAgent,
  hugeTurns,
  imgCount,
  label,
  newSession,
  pdfCount,
  repsLoad,
  seed,
  seedFilesFixture,
} from "./helpers.ts";
import { installMetrics } from "./metrics.ts";

// Load fixtures are immutable during measurement, allowing labels to be
// compared without reseeding the expensive history in every repetition.
test.describe.configure({ mode: "serial", retries: 0, timeout: 300_000 });

let agentId = "";
let hugeSession = "";
let filesSession = "";
const runs: Record<string, any>[] = [];

async function openSession(
  page: import("@playwright/test").Page,
  sessionId: string,
  credentials: any,
  readyText = "cache key derived",
): Promise<void> {
  await installMetrics(page);
  await page.goto(`/agents/${agentId}/sessions/${sessionId}`);
  if (page.url().includes("/login")) {
    await loginWithPassword(page, credentials.admin.email, credentials.admin.password);
    await page.goto(`/agents/${agentId}/sessions/${sessionId}`);
  }
  await expect(page.locator("body")).toContainText(readyText, { timeout: 60_000 });
}

async function mountAll(page: import("@playwright/test").Page, expectedTurns: number): Promise<number> {
  for (let attempt = 0; attempt < 300; attempt += 1) {
    await page.evaluate(() => window.__perf?.scrollTopOnce());
    const count = await page.locator("body").textContent().then((text) => (text?.match(/seed turn /g) ?? []).length);
    if (count >= expectedTurns) return await page.evaluate(() => performance.now());
    await page.waitForTimeout(300);
  }
  throw new Error(`history did not fully mount after 90 seconds, expected ${expectedTurns} turns`);
}

test.beforeAll(async ({ admin }) => {
  agentId = await ensurePerfAgent(admin);
  hugeSession = await newSession(admin, agentId);
  await seed(admin, agentId, hugeSession, hugeTurns);
  const fileFixture = await seedFilesFixture(admin, agentId);
  filesSession = fileFixture.sessionId;
});


test("huge-load", async ({ page, creds }) => {
  for (let repetition = 0; repetition < repsLoad; repetition += 1) {
    await openSession(page, hugeSession, creds);
    const fullMountMs = await mountAll(page, hugeTurns);
    runs.push({
      hugeLoad: {
        ...await page.evaluate(() => window.__perf?.navStats("/messages")),
        ...await page.evaluate(() => window.__perf?.loadStats()),
        fullMountMs,
      },
    });
  }
});

test("files-load", async ({ page, creds }) => {
  for (let repetition = 0; repetition < repsLoad; repetition += 1) {
    await openSession(page, filesSession, creds, "please look at image 1");
    await page.evaluate(() => window.__perf?.scrollTopOnce());
    await expect.poll(async () => page.evaluate(() => window.__perf?.imgProgress()), { timeout: 120_000 }).toMatchObject({
      total: imgCount,
      loaded: imgCount,
    });
    const metrics = await page.evaluate(() => ({
      ...window.__perf?.navStats("file-content"),
      ...window.__perf?.imgProgress(),
      settleMs: Math.round(performance.now()),
    }));
    runs[repetition].filesLoad = metrics;
  }

  await mkdir(resolve("perf", "results"), { recursive: true });
  await writeFile(
    resolve("perf", "results", `load-${label}.json`),
    JSON.stringify(
      {
        label,
        date: new Date().toISOString(),
        commit: process.env.GIT_COMMIT ?? "unknown",
        huge_turns: hugeTurns,
        img_count: imgCount,
        pdf_count: pdfCount,
        runs,
      },
      null,
      2,
    ),
  );
});
