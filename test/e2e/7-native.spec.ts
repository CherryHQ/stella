import type { Page, TestInfo } from "@playwright/test";
import { type ApiClient, expectStatus } from "./lib/api.ts";
import type { Sql } from "./lib/db.ts";
import { expect, test } from "./lib/fixtures.ts";

test.describe.configure({ mode: "serial" });

interface NativePlugin {
  id: string;
  is_enabled: boolean;
}

interface NativePluginList {
  native_plugins: NativePlugin[];
  next_page_token?: string | null;
}

interface NativeAgentDeny {
  native_id: string;
  agent_id: string;
  is_denied: boolean;
}

interface NativeAgentDenyList {
  denials: NativeAgentDeny[];
  next_page_token?: string | null;
}

interface Agent {
  id: string;
  name: string;
}

function nativePath(id: string): string {
  return `/api/native-plugins/${id}`;
}

function nativeDenialsPath(id: string): string {
  return `${nativePath(id)}/agent-denials`;
}

async function listDenials(admin: ApiClient, nativeID: string): Promise<NativeAgentDeny[]> {
  return expectStatus(
    await admin.get<NativeAgentDenyList>(nativeDenialsPath(nativeID)),
    200,
    `list denials for ${nativeID}`,
  ).denials;
}

async function runNativeJourney(
  page: Page,
  admin: ApiClient,
  db: Sql,
  loginAsAdmin: () => Promise<void>,
  testInfo: TestInfo,
  colorScheme: "light" | "dark",
): Promise<void> {
  const nativePlugins = expectStatus(
    await admin.get<NativePluginList>("/api/native-plugins"),
    200,
    "list native capabilities",
  ).native_plugins;
  expect(nativePlugins.length, "native capability registry must expose two selectable capabilities").toBeGreaterThanOrEqual(2);

  const first = nativePlugins[0];
  const second = nativePlugins[1];
  const originalEnabled = first.is_enabled;
  const agentName = `e2e-native-${Date.now()}`;
  const agent = expectStatus(
    await admin.post<Agent>("/api/agents", { name: agentName, enabled: false }),
    201,
    "create native policy Agent",
  );

  try {
    await loginAsAdmin();
    await page.goto("/admin/integrations/native");
    await expect(page).toHaveURL(/\/admin\/integrations\/native$/);
    if (colorScheme === "dark") {
      await expect(page.locator("html")).toHaveClass(/(?:^|\s)dark(?:\s|$)/);
    } else {
      await expect(page.locator("html")).not.toHaveClass(/(?:^|\s)dark(?:\s|$)/);
    }
    await expect(page.getByRole("heading", { name: "Native capabilities", exact: true })).toBeVisible();
    await expect(page.getByText(first.id, { exact: true }).first()).toBeVisible();
    await expect(page.getByText(second.id, { exact: true }).first()).toBeVisible();

    const globalSwitch = page.getByRole("switch", { name: first.id, exact: true });
    await expect(globalSwitch).toBeChecked({ checked: originalEnabled });
    await globalSwitch.click();
    await expect(globalSwitch).toBeChecked({ checked: !originalEnabled });
    await expect
      .poll(async () => {
        const current = expectStatus(
          await admin.get<NativePlugin>(nativePath(first.id)),
          200,
          `get ${first.id}`,
        );
        return current.is_enabled;
      })
      .toBe(!originalEnabled);
    await expect
      .poll(async () => {
        const rows = await db`select enabled from plugin where id = ${first.id}`;
        return rows[0]?.enabled ?? null;
      })
      .toBe(!originalEnabled);

    const capabilitySelect = page.locator('[data-slot="select-trigger"]').first();
    await expect(capabilitySelect).toContainText(first.id);
    const firstAgentRow = page.locator("div.flex.flex-wrap.items-center").filter({ hasText: agentName }).first();
    await expect(firstAgentRow).toBeVisible();
    await expect(firstAgentRow.getByRole("button", { name: /^Deny$/ })).toBeVisible();

    await firstAgentRow.getByRole("button", { name: /^Deny$/ }).click();
    await expect(firstAgentRow.getByRole("button", { name: /^(Remove deny|取消禁用)$/ })).toBeVisible();
    await expect
      .poll(async () => (await listDenials(admin, first.id)).some((deny) => deny.agent_id === agent.id && deny.is_denied))
      .toBe(true);
    await expect
      .poll(async () =>
        Number(
          (await db`select count(*)::int as count from native_agent_deny where native_id = ${first.id} and agent_id = ${agent.id}`)[0]
            ?.count ?? 0,
        )
      )
      .toBe(1);

    await capabilitySelect.click();
    await page.getByRole("option", { name: second.id, exact: true }).click();
    await expect(capabilitySelect).toContainText(second.id);
    await expect
      .poll(async () => (await listDenials(admin, second.id)).some((deny) => deny.agent_id === agent.id))
      .toBe(false);
    const secondAgentRow = page.locator("div.flex.flex-wrap.items-center").filter({ hasText: agentName }).first();
    await expect(secondAgentRow.getByRole("button", { name: /^Deny$/ })).toBeVisible();
    await expect(secondAgentRow.getByRole("button", { name: /^(Remove deny|取消禁用)$/ })).toHaveCount(0);

    await capabilitySelect.click();
    await page.getByRole("option", { name: first.id, exact: true }).click();
    await expect(capabilitySelect).toContainText(first.id);
    await expect(firstAgentRow.getByRole("button", { name: /^(Remove deny|取消禁用)$/ })).toBeVisible();
    await page.screenshot({ path: testInfo.outputPath("native-capabilities.png"), fullPage: true });

    await firstAgentRow.getByRole("button", { name: /^(Remove deny|取消禁用)$/ }).click();
    await expect(firstAgentRow.getByRole("button", { name: /^Deny$/ })).toBeVisible();
    await expect
      .poll(async () => (await listDenials(admin, first.id)).some((deny) => deny.agent_id === agent.id))
      .toBe(false);
    await expect
      .poll(async () =>
        Number(
          (await db`select count(*)::int as count from native_agent_deny where native_id = ${first.id} and agent_id = ${agent.id}`)[0]
            ?.count ?? 0,
        )
      )
      .toBe(0);
  } finally {
    await admin.patch(nativePath(first.id), { is_enabled: originalEnabled });
    await admin.delete(`/api/agents/${agent.id}`);
  }
}

for (const colorScheme of ["light", "dark"] as const) {
  test.describe(`native capability management (${colorScheme})`, () => {
    test.use({ colorScheme });

    test("global switch and Agent deny persist across capability selection", async ({ page, admin, db, loginAsAdmin }, testInfo) => {
      await runNativeJourney(page, admin, db, loginAsAdmin, testInfo, colorScheme);
    });
  });
}
