import { describe, expect, it, vi } from "vitest";

vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({ t: (key: string) => key }),
}));

import { AdminPluginsPage, PersonalToolsPage } from "./PluginsPage";
import { adminSettingsNav } from "@/features/settings/AdminLayout";
import { personalSettingsGroups } from "@/features/settings/SettingsLayout";
import { Route as PersonalPluginsRoute } from "@/routes/_app/settings/plugins.lazy";
import { Route as AdminPluginsRoute } from "@/routes/_app/admin/integrations/plugins.lazy";

describe("plugin surface ownership", () => {
  it("keeps personal MCP available to admins through Personal Settings", () => {
    const personalLinks = personalSettingsGroups(true).flatMap((group) =>
      group.items.map((item) => item.href),
    );

    expect(personalLinks).toContain("/settings/plugins");
    expect(PersonalPluginsRoute.options.component).toBe(PersonalToolsPage);
  });

  it("keeps deployment plugin management on Admin Console without personal MCP", () => {
    const adminLinks = adminSettingsNav.flatMap((group) => group.items.map((item) => item.href));

    expect(adminLinks).toContain("/admin/integrations/plugins");
    expect(adminLinks).not.toContain("/settings/plugins");
    expect(AdminPluginsRoute.options.component).toBe(AdminPluginsPage);
    expect(AdminPluginsPage).not.toBe(PersonalToolsPage);
  });
});
