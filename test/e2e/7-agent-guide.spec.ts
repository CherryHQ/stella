import { expectStatus } from "./lib/api.ts";
import { expect, test } from "./lib/fixtures.ts";
import type { PluginResource } from "./lib/types.ts";

function pluginPath(id: string): string {
  return `/api/plugins/${encodeURIComponent(id)}`;
}

async function systemEmail(
  admin: import("./lib/api.ts").ApiClient,
): Promise<PluginResource> {
  const list = expectStatus(
    await admin.get<{ plugins: PluginResource[]; }>("/api/plugins?scope=system"),
    200,
    "list system plugin resources",
  );
  const resource = list.plugins.find((item) => item.name === "email");
  if (!resource) {
    throw new Error(`email resource missing: ${JSON.stringify(list.plugins)}`);
  }
  return resource;
}

test("admin can open the bare email guide and persist its raw resource switch", async ({ page, admin, loginAsAdmin }) => {
  const original = await systemEmail(admin);
  const requests: string[] = [];
  page.on("response", (response) => {
    const pathname = new URL(response.url()).pathname;
    const decodedPath = decodeURIComponent(pathname);
    if (decodedPath === `/api/plugins/${original.id}`) {
      requests.push(`${response.request().method()} ${decodedPath}`);
    }
  });
  try {
    await loginAsAdmin();
    await page.goto(
      `/admin/integrations/plugins/${encodeURIComponent(original.id)}`,
    );
    await expect(page).toHaveURL(/\/admin\/integrations\/plugins\/.+$/);
    await expect(page.getByRole("heading", { name: /email/i })).toBeVisible();
    const toggle = page.getByRole("switch").first();
    await expect(toggle).toBeChecked({ checked: original.is_enabled });
    await toggle.click();
    await expect
      .poll(async () => (await systemEmail(admin)).is_enabled)
      .toBe(!original.is_enabled);
    const current = await systemEmail(admin);
    await admin.patch(pluginPath(original.id), {
      is_enabled: original.is_enabled,
      expected_settings_digest: current.settings_digest,
    });
    await expect
      .poll(async () => (await systemEmail(admin)).is_enabled)
      .toBe(original.is_enabled);
    expect(
      requests.every(
        (request) => request === `PATCH /api/plugins/${original.id}`,
      ),
    ).toBe(true);
  } finally {
    const current = await systemEmail(admin);
    if (current.is_enabled !== original.is_enabled) {
      await admin.patch(pluginPath(original.id), {
        is_enabled: original.is_enabled,
        expected_settings_digest: current.settings_digest,
      });
    }
  }
});
