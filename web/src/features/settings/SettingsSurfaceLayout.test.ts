import { describe, expect, it, vi } from "vitest";

vi.stubGlobal("localStorage", {
  getItem: () => "en",
  setItem: () => undefined,
});

const { adminSettingsNav } = await import("@/features/settings/AdminLayout");
const { personalSettingsGroups } = await import("@/features/settings/SettingsLayout");
const { findActiveSettingsNavItem } = await import("@/features/settings/SettingsSurfaceLayout");

describe("findActiveSettingsNavItem", () => {
  it("keeps Personal Settings rows active on detail descendants", () => {
    const groups = personalSettingsGroups(false);

    expect(findActiveSettingsNavItem(groups, "/settings/skills")?.href).toBe("/settings/skills");
    expect(findActiveSettingsNavItem(groups, "/settings/skills/catalog/example")?.href).toBe(
      "/settings/skills",
    );
    expect(findActiveSettingsNavItem(groups, "/settings/skills-old")).toBeUndefined();
  });

  it("keeps Admin Console rows active on detail descendants", () => {
    expect(
      findActiveSettingsNavItem(adminSettingsNav, "/admin/integrations/plugins/example")?.href,
    ).toBe("/admin/integrations/plugins");
    expect(
      findActiveSettingsNavItem(adminSettingsNav, "/admin/resources/library/folder/runbooks")?.href,
    ).toBe("/admin/resources/library");
  });
});
