import { expectStatus } from "./lib/api.ts";
import { expect, test } from "./lib/fixtures.ts";

interface PluginDefinition {
  id: string;
  display_name: string;
}

interface PluginConfig {
  id: string;
  plugin_id: string;
  scope: string;
  is_enabled: boolean | null;
  revision: number;
}

function isEmailConfigMutation(response: import("@playwright/test").Response): boolean {
  const request = response.request();
  const pathname = new URL(response.url()).pathname;
  return request.method() === "PATCH" && /^\/api\/plugins\/email\/configs(?:\/|$)/.test(pathname);
}

async function systemEmailConfig(admin: import("./lib/api.ts").ApiClient): Promise<PluginConfig> {
  const list = expectStatus(
    await admin.get<{ configs: PluginConfig[]; }>("/api/plugins/email/configs?scope=system"),
    200,
    "list email system configs",
  );
  const config = list.configs.find((item) => item.plugin_id === "email" && item.scope === "system");
  if (!config) throw new Error(`email system config missing: ${JSON.stringify(list.configs)}`);
  return config;
}

test("admin can open the bare email guide and persist its config", async ({ page, admin, loginAsAdmin }) => {
  const plugins = expectStatus(
    await admin.get<{ plugins: PluginDefinition[]; }>("/api/plugins"),
    200,
    "list plugins",
  );
  expect(plugins.plugins.find((plugin) => plugin.id === "email")?.display_name).toBeTruthy();

  const original = await systemEmailConfig(admin);
  const configRequests: string[] = [];
  const config404s: string[] = [];
  page.on("response", (response) => {
    const pathname = new URL(response.url()).pathname;
    if (!pathname.startsWith("/api/plugins/email/configs")) return;
    configRequests.push(pathname);
    if (response.status() === 404) config404s.push(`${response.request().method()} ${pathname}`);
  });

  try {
    await loginAsAdmin();
    await page.goto("/admin/integrations/plugins/email");
    await expect(page).toHaveURL(/\/admin\/integrations\/plugins\/email$/);
    await expect(page.getByRole("heading", { name: "email", exact: true })).toBeVisible();
    await expect(page.getByText("Configuration", { exact: true })).toBeVisible();

    const configSwitch = page.getByRole("switch").first();
    await expect(configSwitch).toBeChecked({ checked: original.is_enabled === true });

    const edit = page.getByRole("button", { name: "Edit", exact: true }).first();
    await edit.click();
    const editor = page.getByRole("dialog").last();
    const saveResponse = page.waitForResponse(isEmailConfigMutation);
    await editor.getByRole("button", { name: "Save", exact: true }).click();
    expect((await saveResponse).status()).toBe(200);

    const toggledResponse = page.waitForResponse(isEmailConfigMutation);
    await configSwitch.click();
    expect((await toggledResponse).status()).toBe(200);
    await expect.poll(async () => (await systemEmailConfig(admin)).is_enabled).toBe(!Boolean(original.is_enabled));

    if (original.is_enabled === null) {
      const inheritResponse = page.waitForResponse(isEmailConfigMutation);
      await page.getByRole("button", { name: "Inherit", exact: true }).first().click();
      expect((await inheritResponse).status()).toBe(200);
    } else {
      const restoredResponse = page.waitForResponse(isEmailConfigMutation);
      await configSwitch.click();
      expect((await restoredResponse).status()).toBe(200);
    }
    await expect.poll(async () => (await systemEmailConfig(admin)).is_enabled).toBe(original.is_enabled);

    expect(configRequests.length).toBeGreaterThanOrEqual(3);
    expect(configRequests.every((path) => /^\/api\/plugins\/email\/configs(?:\/|$)/.test(path))).toBe(true);
    expect(config404s).toEqual([]);
  } finally {
    const current = await systemEmailConfig(admin);
    if (current.is_enabled !== original.is_enabled) {
      await admin.patch(`/api/plugins/email/configs/${current.id}`, {
        expected_revision: current.revision,
        is_enabled: original.is_enabled,
      });
    }
  }
});
