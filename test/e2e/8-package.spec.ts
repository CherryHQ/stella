import { resolve } from "node:path";
import { expectStatus } from "./lib/api.ts";
import { repoRoot } from "./lib/env.ts";
import { expect, test } from "./lib/fixtures.ts";

interface PluginDefinition {
  id: string;
  revision: number;
  spec: Record<string, unknown>;
  lifecycle_status: "installed" | "in_use" | "cleanup_pending";
  retired_at?: string;
}

interface PluginConfig {
  id: string;
  revision: number;
  is_enabled: boolean | null;
}

interface CopiedSkill {
  id: string;
  description: string;
  source?: string;
  content_digest?: string;
}

test("admin imports and previews a directory package before updating it", async ({ page, admin, loginAsAdmin }) => {
  const packageV1 = resolve(repoRoot, "test/e2e/fixtures/package-v1");
  const packageV2 = resolve(repoRoot, "test/e2e/fixtures/package-v2");
  let definition: PluginDefinition | undefined;
  let config: PluginConfig | undefined;
  let copiedSkill: CopiedSkill | undefined;

  try {
    await loginAsAdmin();
    await page.goto("/admin/integrations/plugins");
    await expect(page.getByRole("button", { name: /^(Import package|导入插件包)$/ })).toBeVisible();
    await page.getByRole("button", { name: /^(Import package|导入插件包)$/ }).click();
    await page.getByPlaceholder(/\/srv\/stella\/plugins\/example/).fill(packageV1);
    await page.getByRole("button", { name: /^(Import package|导入插件包)$/ }).last().click();
    await expect(page.getByText(/Plugin package imported|插件包已导入/)).toBeVisible();

    const imported = expectStatus(
      await admin.get<{ plugins: PluginDefinition[]; }>("/api/plugins"),
      200,
      "list imported package",
    ).plugins.find((item) => item.id === "e2e.package");
    if (!imported) throw new Error("imported package is missing");
    definition = imported;
    expect(definition.lifecycle_status).toBe("installed");
    config = expectStatus(
      await admin.get<{ configs: PluginConfig[]; }>("/api/plugins/e2e.package/configs?scope=system"),
      200,
      "get imported package config",
    ).configs[0];
    if (!config) throw new Error("imported package config is missing");

    await page.goto("/admin/integrations/plugins/e2e.package");
    await expect(page.getByRole("dialog").getByText("Package installed", { exact: true })).toBeVisible();
    await expect(page.getByText("e2e-skill", { exact: true })).toBeVisible();
    const copyResponse = page.waitForResponse((response) =>
      response.request().method() === "POST"
      && response.url().endsWith("/api/skills/copy-package")
      && response.status() === 201
    );
    await page.getByRole("button", { name: /^(Copy to my Skills|复制到我的 Skill)$/ }).click();
    const copiedResponse = await copyResponse;
    const copiedBody = await copiedResponse.json() as CopiedSkill;
    if (!copiedBody.id) throw new Error("copy response did not return a Skill id");
    await expect(page).toHaveURL(/\/settings\/skills(?:\?.*)?$/);
    await expect(page.getByRole("button", { name: /^(Save|保存)$/ })).toBeVisible();
    const copiedID = copiedBody.id;
    const description = page.locator("textarea").first();
    await description.fill("Independent copied skill");
    const saveResponse = page.waitForResponse((response) =>
      response.request().method() === "PATCH"
      && response.url().includes("/api/agents/")
      && response.url().includes("/skills/")
      && response.status() === 200
    );
    await page.getByRole("button", { name: /^(Save|保存)$/ }).click();
    await saveResponse;
    const copied = expectStatus(
      await admin.get<CopiedSkill & { scope: string; }>(`/api/skills/${copiedID}`),
      200,
      "get copied skill",
    );
    copiedSkill = copied;
    expect(copied.scope).toBe("user");
    expect(copied.description).toBe("Independent copied skill");
    expect(copied.source).toContain("plugin:e2e.package/e2e-skill@");

    const packageDigest = (definition.spec.content as { digest?: string; }).digest;
    if (!packageDigest) throw new Error("package content digest is missing");
    expectStatus(
      await admin.post("/api/skills/copy-package", {
        source_id: definition.id,
        expected_digest: `sha256:${"0".repeat(64)}`,
        skill_name: "e2e-skill",
        scope: "user",
      }),
      409,
      "reject stale package digest",
    );
    expectStatus(
      await admin.post("/api/skills/copy-package", {
        source_id: definition.id,
        expected_digest: packageDigest,
        skill_name: "missing-skill",
        scope: "user",
      }),
      404,
      "hide undeclared package Skill",
    );
    expectStatus(
      await admin.post("/api/skills/copy-package", {
        source_id: definition.id,
        expected_digest: "bad-digest",
        skill_name: "e2e-skill",
        scope: "user",
      }),
      400,
      "reject malformed package digest",
    );
    expectStatus(
      await admin.post("/api/skills/copy-package", {
        source_id: definition.id,
        expected_digest: packageDigest,
        skill_name: "e2e-skill",
        scope: "user_agent",
        agent_id: "missing-target-agent",
      }),
      403,
      "deny inaccessible target agent",
    );
    expectStatus(
      await admin.post("/api/skills/copy-package", {
        source_id: definition.id,
        expected_digest: packageDigest,
        skill_name: "e2e-skill",
        scope: "user",
      }),
      409,
      "reject duplicate copied Skill",
    );

    await page.goto("/admin/integrations/plugins/e2e.package");
    await expect(page.getByRole("dialog").getByText("Package installed", { exact: true })).toBeVisible();
    await page.getByRole("button", { name: /^(Update package|更新插件包)$/ }).click();
    await page.getByPlaceholder(/\/srv\/stella\/plugins\/example/).fill(packageV2);
    await page.getByRole("button", { name: /^(Preview|预览变更)$/ }).click();
    await expect(page.getByText(/Candidate version: 2\.0\.0|候选版本：2\.0\.0/)).toBeVisible();
    await expect(page.getByText(/github: \+repo|github： \+repo/)).toBeVisible();
    await page.getByRole("button", { name: /^(Update package|更新插件包)$/ }).last().click();

    await expect.poll(async () => (await admin.get<PluginDefinition>("/api/plugins/e2e.package")).body.revision).toBe(
      definition.revision + 1,
    );
    const updated = expectStatus(
      await admin.get<PluginDefinition>("/api/plugins/e2e.package"),
      200,
      "get updated package",
    );
    expect(updated.spec.origin).toBe("package");
    const unchangedCopy = expectStatus(
      await admin.get<CopiedSkill>(`/api/skills/${copiedSkill.id}`),
      200,
      "verify copied skill survives package update",
    );
    expect(unchangedCopy.description).toBe("Independent copied skill");

    const currentConfig = expectStatus(
      await admin.get<{ configs: PluginConfig[]; }>("/api/plugins/e2e.package/configs?scope=system"),
      200,
      "get config before disabling",
    ).configs[0];
    expectStatus(
      await admin.patch(`/api/plugins/e2e.package/configs/${currentConfig.id}`, {
        expected_revision: currentConfig.revision,
        is_enabled: false,
      }),
      200,
      "disable package config",
    );
    const disabled = expectStatus(await admin.get<PluginDefinition>("/api/plugins/e2e.package"), 200, "get disabled package");
    expect(disabled.lifecycle_status).toBe("installed");
    expect(disabled.retired_at).toBeFalsy();
    await page.reload();
    await expect(page.getByRole("dialog").getByText("Installed", { exact: true })).toBeVisible();
    await expect(page.getByRole("switch").first()).not.toBeChecked();

    await page.getByRole("button", { name: "Delete", exact: true }).last().click();
    const removal = page.waitForResponse((response) =>
      response.request().method() === "DELETE"
      && new URL(response.url()).pathname === "/api/plugins/e2e.package"
    );
    await page.getByRole("dialog", { name: "Remove plugin?", exact: true })
      .getByRole("button", { name: "Delete", exact: true }).click();
    expect((await removal).status()).toBe(204);
    await expect(page.getByText("Plugin removal requested.", { exact: true })).toBeVisible();
    const removed = await admin.get<PluginDefinition>("/api/plugins/e2e.package");
    expect([200, 404]).toContain(removed.status);
    if (removed.status === 200) {
      expect(removed.body.retired_at).toBeTruthy();
      expect(["in_use", "cleanup_pending"]).toContain(removed.body.lifecycle_status);
      await expect(page.getByRole("button", { name: "Update package", exact: true })).toBeDisabled();
      await expect(page.getByRole("switch").first()).toBeDisabled();
      await expect(page.getByRole("button", { name: "Refresh", exact: true })).toBeVisible();
    }
  } finally {
    if (copiedSkill) {
      const currentSkill = await admin.get<CopiedSkill>(`/api/skills/${copiedSkill.id}`);
      if (currentSkill.status === 200 && currentSkill.body.content_digest) {
        await admin.delete(
          `/api/skills/${copiedSkill.id}?expected_digest=${currentSkill.body.content_digest}`,
        );
      }
    }
    const current = await admin.get<PluginDefinition>("/api/plugins/e2e.package");
    if (current.status === 200) {
      const currentDefinition = current.body;
      const configs = await admin.get<{ configs: PluginConfig[]; }>("/api/plugins/e2e.package/configs?scope=system");
      for (const item of configs.body.configs ?? []) {
        await admin.delete(`/api/plugins/e2e.package/configs/${item.id}?expected_revision=${item.revision}`);
      }
      await admin.delete(`/api/plugins/e2e.package?expected_revision=${currentDefinition.revision}`);
    }
  }
});
