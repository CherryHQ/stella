import { ensureAgent } from "./lib/agent.ts";
import { expectStatus } from "./lib/api.ts";
import { expect, test } from "./lib/fixtures.ts";
import { ensureProvider } from "./lib/provider.ts";

interface Channel {
  id: string;
  name: string;
  type: string;
  agent_id?: string;
  enabled: boolean;
  config: string;
}

test.describe.configure({ mode: "serial" });

let channelId = "";

test.afterAll(async ({ admin }) => {
  if (channelId) await admin.delete(`/api/channels/${channelId}`);
});

test("channel drafts survive a query refetch and reach the agent profile consumer", async ({ page, admin, loginAsAdmin }) => {
  const { modelRef } = await ensureProvider(admin);
  const agentId = await ensureAgent(admin, modelRef, `e2e-channel-${Date.now()}`);
  const created = expectStatus(
    await admin.post<Channel>("/api/channels", {
      name: "channel-query-base",
      type: "telegram",
      agent_id: agentId,
      enabled: true,
      config: "{}",
    }),
    201,
    "create channel",
  );
  channelId = created.id;

  await loginAsAdmin();
  // Warm the profile consumer first. The later settings navigation stays in
  // this SPA so both surfaces share the same QueryClient cache.
  await page.goto(`/agents/${agentId}/profile?tab=channels`);
  await expect(page.getByText("channel-query-base", { exact: true })).toBeVisible();
  await page.locator("button.w-full.min-w-0.justify-start").click();
  await page.locator('a[href="/settings"]').click();
  await expect(page).toHaveURL(/\/settings(?:\/account)?$/);
  await page.locator('a[href="/settings/channels"]').click();
  await expect(page).toHaveURL(/\/settings\/channels$/);
  await page.locator(`a[href="/settings/channels/${channelId}"]`).click();
  await expect(page).toHaveURL(new RegExp(`/settings/channels/${channelId}$`));
  // ChannelFields' label is visual only, so use the first native text input.
  const name = page.locator('input[type="text"]').first();
  await expect(name).toHaveValue("channel-query-base");
  let releasePatch!: () => void;
  let patchStarted = false;
  let refetchStarted = false;
  let releaseRefetch!: () => void;
  const patchBlocked = new Promise<void>((resolve) => {
    releasePatch = resolve;
  });
  const refetchBlocked = new Promise<void>((resolve) => {
    releaseRefetch = resolve;
  });
  await page.route(`**/api/channels/${channelId}`, async (route) => {
    if (route.request().method() === "PATCH") {
      patchStarted = true;
      await patchBlocked;
    }
    await route.continue();
  });
  await page.route("**/api/channels", async (route) => {
    if (route.request().method() === "GET") {
      refetchStarted = true;
      await refetchBlocked;
    }
    await route.continue();
  });
  await name.fill("channel-query-inflight-first");
  await page.getByRole("button", { name: "Save" }).click();
  await expect.poll(() => patchStarted).toBe(true);
  // The first request is still in flight. A newer draft must win when it
  // eventually triggers the cache invalidation refetch.
  await name.fill("channel-query-inflight-second");
  releasePatch();
  await expect.poll(() => refetchStarted).toBe(true);
  releaseRefetch();
  await expect(page.locator("body")).toContainText("channel-query-inflight-first");
  await expect(name).toHaveValue("channel-query-inflight-second");
  await expect.poll(async () => (await admin.get<Channel>(`/api/channels/${channelId}`)).body.name).toBe("channel-query-inflight-first");
  await expect(page.getByRole("button", { name: "Save" })).toBeEnabled();
  await expect(name).toHaveValue("channel-query-inflight-second");
  await page.unroute(`**/api/channels/${channelId}`);
  await page.unroute("**/api/channels");

  let failedSave = false;
  await page.route(`**/api/channels/${channelId}`, async (route) => {
    if (route.request().method() === "PATCH") {
      failedSave = true;
      await route.fulfill({
        status: 500,
        contentType: "application/json",
        body: JSON.stringify({ error: "synthetic save failure" }),
      });
      return;
    }
    await route.continue();
  });
  await name.fill("channel-query-failed");
  await page.getByRole("button", { name: "Save" }).click();
  await expect.poll(() => failedSave).toBe(true);
  await expect(page.getByRole("button", { name: "Save" })).toBeEnabled();
  await expect(name).toHaveValue("channel-query-failed");
  await page.unroute(`**/api/channels/${channelId}`);

  await name.fill("channel-query-inflight-second");
  await page.getByRole("button", { name: "Save" }).click();
  await expect.poll(async () => (await admin.get<Channel>(`/api/channels/${channelId}`)).body.name).toBe("channel-query-inflight-second");

  // Return through browser history rather than a new page load, proving the
  // profile consumer sees the cache invalidation from ChannelsPage.
  await page.goBack();
  await page.goBack();
  await page.goBack();
  await expect(page).toHaveURL(new RegExp(`/agents/${agentId}/profile`));
  await expect(page.getByText("channel-query-inflight-second", { exact: true })).toBeVisible();
});
