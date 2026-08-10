import { describe, expect, it } from "vitest";
import { adminCompatibilityHref } from "@/lib/admin-routes";

describe("adminCompatibilityHref", () => {
  it.each([
    ["/settings/providers", "", "/admin/ai/providers"],
    ["/settings/providers/openai", "?tab=models", "/admin/ai/providers/openai?tab=models"],
    ["/settings/embedding", "", "/admin/ai/embedding"],
    ["/settings/vision", "?model=current", "/admin/ai/vision?model=current"],
    ["/settings/provisioning", "", "/admin/access/provisioning"],
    ["/settings/users", "?state=active", "/admin/users?state=active"],
    ["/settings/users/user-1", "", "/admin/users/user-1"],
    ["/settings/plugins", "", "/admin/integrations/plugins"],
    [
      "/settings/plugins/telegram",
      "?tab=config",
      "/admin/integrations/plugins/telegram?tab=config",
    ],
    ["/settings/about", "", "/admin/overview"],
  ])("maps %s%s", (pathname, search, expected) => {
    expect(adminCompatibilityHref(pathname, search)).toBe(expected);
  });

  it("does not redirect personal routes", () => {
    expect(adminCompatibilityHref("/settings/account")).toBeNull();
    expect(adminCompatibilityHref("/settings/credentials")).toBeNull();
  });
});
