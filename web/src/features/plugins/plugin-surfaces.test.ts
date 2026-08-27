import { describe, expect, it, vi } from "vitest";

vi.hoisted(() => {
  Object.defineProperty(globalThis, "localStorage", {
    configurable: true,
    value: { getItem: () => "en", setItem: () => undefined },
  });
});

import { AdminPluginsPage } from "./PluginsPage";
import {
  PersonalCredentialsPage,
  SystemCredentialsPage,
} from "@/features/credentials/CredentialsPage";
import { GlobalMCPPage, PersonalMCPPage } from "@/features/mcp/MCPServersPage";
import { GlobalLibraryPage, SettingsLibraryPage } from "@/features/library/LibraryFilesPage";
import { GlobalSkillsPage, PersonalSkillsPage } from "@/features/skills/SkillsPage";
import { adminSettingsNav } from "@/features/settings/AdminLayout";
import { personalSettingsGroups } from "@/features/settings/SettingsLayout";
import { Route as PersonalMCPRoute } from "@/routes/_app/settings/mcp.lazy";
import { Route as AdminPluginsRoute } from "@/routes/_app/admin/integrations/plugins.lazy";
import { Route as AdminMCPRoute } from "@/routes/_app/admin/resources/mcp.lazy";
import { Route as PersonalSkillsRoute } from "@/routes/_app/settings/skills.lazy";
import { Route as AdminSkillsRoute } from "@/routes/_app/admin/resources/skills.lazy";
import { Route as PersonalCredentialsRoute } from "@/routes/_app/settings/credentials.lazy";
import { Route as AdminCredentialsRoute } from "@/routes/_app/admin/resources/credentials.lazy";
import { Route as PersonalLibraryRoute } from "@/routes/_app/settings/library.lazy";
import { Route as AdminLibraryRoute } from "@/routes/_app/admin/resources/library.lazy";

describe("plugin surface ownership", () => {
  it("keeps personal MCP available to admins through Personal Settings", () => {
    const personalLinks = personalSettingsGroups(true).flatMap((group) =>
      group.items.map((item) => item.href),
    );

    expect(personalLinks).toContain("/settings/mcp");
    expect(PersonalMCPRoute.options.component).toBe(PersonalMCPPage);
    expect(PersonalSkillsRoute.options.component).toBe(PersonalSkillsPage);
    expect(PersonalCredentialsRoute.options.component).toBe(PersonalCredentialsPage);
    expect(PersonalLibraryRoute.options.component).toBe(SettingsLibraryPage);
  });

  it("keeps deployment plugin management on Admin Console without personal MCP", () => {
    const adminLinks = adminSettingsNav.flatMap((group) => group.items.map((item) => item.href));

    expect(adminLinks).toContain("/admin/integrations/plugins");
    expect(adminLinks).toContain("/admin/resources/mcp");
    expect(adminLinks).not.toContain("/settings/mcp");
    expect(AdminPluginsRoute.options.component).toBe(AdminPluginsPage);
    expect(AdminMCPRoute.options.component).toBe(GlobalMCPPage);
    expect(AdminSkillsRoute.options.component).toBe(GlobalSkillsPage);
    expect(AdminCredentialsRoute.options.component).toBe(SystemCredentialsPage);
    expect(AdminLibraryRoute.options.component).toBe(GlobalLibraryPage);
    expect(AdminPluginsPage).not.toBe(GlobalMCPPage);
  });
});
