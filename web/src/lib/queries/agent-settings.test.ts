import { beforeEach, describe, expect, it, vi } from "vitest";
import * as sdkModule from "@/lib/api-client/sdk.gen";
import * as authUsersModule from "@/lib/auth-users";

/**
 * These requests used to be wrapped in `.catch(() => [])`, so an unreachable
 * server produced a fully-populated success shape with empty lists: no agents,
 * no models, and — worst — a blank soul/profile draft that overwrites real
 * memory the moment the user hits save. The loader must reject so the route's
 * `errorComponent` can say what actually happened.
 */

// SAFETY: the SDK spy only needs the response data; transport metadata is irrelevant to these tests.
const ok = <T>(data: T) => Promise.resolve({ data }) as never;

const sdk = {
  listAgents: vi.spyOn(sdkModule, "listAgents"),
  listModels: vi.spyOn(sdkModule, "listModels"),
  getMe: vi.spyOn(sdkModule, "getMe"),
  listBuiltinResources: vi.spyOn(sdkModule, "listBuiltinResources"),
  listAgentSkills: vi.spyOn(sdkModule, "listAgentSkills"),
  listProfileMemories: vi.spyOn(sdkModule, "listProfileMemories"),
};
const authUsers = { fetchAllAuthUsers: vi.spyOn(authUsersModule, "fetchAllAuthUsers") };

const { loadAgentsSettingsData } = await import("./agent-settings");

beforeEach(() => {
  vi.clearAllMocks();
  sdk.listAgents.mockReturnValue(ok({ agents: [{ id: "a1", name: "Ada" }] }));
  sdk.listModels.mockReturnValue(ok({ models: [] }));
  sdk.getMe.mockReturnValue(ok({ id: "u1", is_admin: false }));
  sdk.listBuiltinResources.mockReturnValue(ok({ resources: [] }));
  sdk.listAgentSkills.mockReturnValue(ok({ skills: [] }));
  sdk.listProfileMemories.mockReturnValue(
    ok({ memories: [{ agent_id: "a1", soul: "be kind", content: "prefers Go" }] }),
  );
  // SAFETY: the fixture only exercises the user fields consumed by this loader.
  authUsers.fetchAllAuthUsers.mockResolvedValue([{ id: "u1", name: "Ada" }] as never);
});

describe("loadAgentsSettingsData", () => {
  it("loads the editor payload when every request succeeds", async () => {
    const data = await loadAgentsSettingsData("a1");
    expect(data.agents).toHaveLength(1);
    expect(data.personalisation.soul).toBe("be kind");
  });

  it.each([
    ["listAgents", () => sdk.listAgents],
    ["listModels", () => sdk.listModels],
    ["getMe", () => sdk.getMe],
    ["listBuiltinResources", () => sdk.listBuiltinResources],
    ["listAgentSkills", () => sdk.listAgentSkills],
  ])("rejects instead of returning an empty payload when %s fails", async (_name, pick) => {
    pick().mockRejectedValue(new Error("network down"));
    await expect(loadAgentsSettingsData("a1")).rejects.toThrow("network down");
  });

  it("rejects rather than handing the editor a blank memory draft", async () => {
    sdk.listProfileMemories.mockRejectedValue(new Error("network down"));
    await expect(loadAgentsSettingsData("a1")).rejects.toThrow("network down");
  });

  /**
   * The user list is fetched only for admins, so a suite that runs entirely as a
   * non-admin never executes that call — the `.catch(() => [])` this loader used
   * to carry could be put back and every test above would still pass. These two
   * are the ones that walk the branch.
   */
  describe("as an admin", () => {
    beforeEach(() => {
      sdk.getMe.mockReturnValue(ok({ id: "u1", is_admin: true }));
    });

    it("loads the users an admin can assign", async () => {
      const data = await loadAgentsSettingsData("a1");
      expect(authUsers.fetchAllAuthUsers).toHaveBeenCalled();
      expect(data.allUsers).toHaveLength(1);
    });

    it("rejects instead of showing an admin an empty user list", async () => {
      authUsers.fetchAllAuthUsers.mockRejectedValue(new Error("network down"));
      await expect(loadAgentsSettingsData("a1")).rejects.toThrow("network down");
    });
  });

  it("does not fetch the user list for a non-admin", async () => {
    const data = await loadAgentsSettingsData("a1");
    expect(authUsers.fetchAllAuthUsers).not.toHaveBeenCalled();
    expect(data.allUsers).toEqual([]);
  });
});
