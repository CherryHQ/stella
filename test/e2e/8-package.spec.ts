// Package import, file-level CAS editing, declared Skill copy, and cleanup.
import { readFile, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { expectStatus } from "./lib/api.ts";
import { repoRoot } from "./lib/env.ts";
import { expect, test } from "./lib/fixtures.ts";
import { loadTestbedState } from "./lib/testbed.ts";
import type { PluginResource } from "./lib/types.ts";

type FileContent = {
  path: string;
  content_base64: string;
  is_executable: boolean;
  resource_digest: string;
};
type CopiedSkill = {
  id: string;
  description: string;
  source?: string;
  content_digest?: string;
};
const decode = (value: string) => Buffer.from(value, "base64").toString("utf8");
const encode = (value: string) => Buffer.from(value, "utf8").toString("base64");

test("admin imports a package, edits declaration files with CAS, and copies its Skill", async ({ page, admin, loginAsAdmin }) => {
  const packageV1 = resolve(repoRoot, "test/e2e/fixtures/package-v1");
  let resource: PluginResource | undefined;
  let copied: CopiedSkill | undefined;
  try {
    const imported = expectStatus(
      await admin.post<PluginResource>("/api/plugins/import", {
        source_path: packageV1,
        scope: "system",
      }),
      201,
      "import package",
    );
    resource = imported;
    expect(resource.name).toBe("e2e.package");
    expect(resource.version).toBe("1.0.0");
    expect(resource.files.some((file) => file.path === "plugin.json")).toBe(
      true,
    );
    const pluginFile = expectStatus(
      await admin.get<FileContent>(
        `/api/plugins/${resource.id}/file?path=plugin.json`,
      ),
      200,
      "read package manifest",
    );
    expect(JSON.parse(decode(pluginFile.content_base64))).toMatchObject({
      name: "e2e.package",
      version: "1.0.0",
    });

    await loginAsAdmin();
    await page.goto(
      `/admin/integrations/plugins/${encodeURIComponent(resource.id)}`,
    );
    await expect(page.getByText("e2e-skill", { exact: true })).toBeVisible();
    await expect(
      page.getByText(/e2e\.package · v1\.0\.0/, { exact: true }),
    ).toBeVisible();

    copied = expectStatus(
      await admin.post<CopiedSkill>("/api/skills/copy-package", {
        source_id: resource.id,
        expected_digest: resource.content_digest,
        skill_name: "e2e-skill",
        scope: "user",
      }),
      201,
      "copy package Skill",
    );
    expect(resource.content_digest).toMatch(/^sha256:[a-f0-9]{64}$/);
    expect(copied.content_digest).toMatch(/^[a-f0-9]{64}$/);
    expect(copied.source).toBe(
      `plugin:${resource.id}/e2e-skill@${resource.content_digest}`,
    );

    const updatedManifest = JSON.stringify(
      {
        $schema: "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
        name: "e2e.package",
        version: "2.0.0",
        description: "E2E package edited",
        extensions: { "com.cherryhq.stella": { version: "1" } },
      },
      null,
      2,
    ) + "\n";
    const edited = expectStatus(
      await admin.patch<PluginResource>(`/api/plugins/${resource.id}/file`, {
        path: "plugin.json",
        content_base64: encode(updatedManifest),
        expected_digest: pluginFile.resource_digest,
      }),
      200,
      "edit package manifest",
    );
    expect(edited.version).toBe("2.0.0");
    expect(
      (
        await admin.patch(`/api/plugins/${resource.id}/file`, {
          path: "plugin.json",
          content_base64: encode(updatedManifest),
          expected_digest: pluginFile.resource_digest,
        })
      ).status,
    ).toBe(409);
    resource = expectStatus(
      await admin.get<PluginResource>(`/api/plugins/${resource.id}`),
      200,
      "get edited package",
    );
    expect(resource.version).toBe("2.0.0");

    const manifestPath = resolve(
      dirname(loadTestbedState().credentialsPath),
      ".agents",
      "plugins",
      "e2e.package",
      "plugin.json",
    );
    const diskV2 = JSON.parse(await readFile(manifestPath, "utf8")) as {
      version: string;
    };
    expect(diskV2.version).toBe("2.0.0");
    const manifestV3 = JSON.stringify({
      $schema: "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
      name: "e2e.package",
      version: "3.0.0",
      description: "E2E package edited externally",
      extensions: { "com.cherryhq.stella": { version: "1" } },
    }) + "\n";
    await writeFile(manifestPath, manifestV3, "utf8");
    resource = expectStatus(
      await admin.get<PluginResource>(`/api/plugins/${resource.id}`),
      200,
      "observe externally edited package",
    );
    expect(resource.version).toBe("3.0.0");
    expect(resource.content_digest).not.toBe(edited.content_digest);
    await page.reload();
    await expect(
      page.getByText(/e2e\.package · v3\.0\.0/, { exact: true }),
    ).toBeVisible();
  } finally {
    if (copied?.content_digest) {
      await admin.delete(
        `/api/skills/${copied.id}?expected_digest=${encodeURIComponent(copied.content_digest)}`,
      );
    }
    if (resource) {
      const current = await admin.get<PluginResource>(
        `/api/plugins/${resource.id}`,
      );
      if (current.status === 200) {
        const deleted = await admin.delete(
          `/api/plugins/${resource.id}?expected_digest=${encodeURIComponent(current.body.content_digest)}`,
        );
        expect([204, 404]).toContain(deleted.status);
      }
    }
  }
});
