import { beforeEach, describe, expect, it, vi } from "vitest";

/**
 * These requests used to be wrapped in `.catch(() => [])`, so an unreachable
 * server produced a fully-populated success shape with empty lists: no agents,
 * no models, and — worst — a blank soul/profile draft that overwrites real
 * memory the moment the user hits save. The loader must reject so the route's
 * `errorComponent` can say what actually happened.
 */

const ok = <T>(data: T) => Promise.resolve({ data });

const sdk = vi.hoisted(() => ({
  listAgents: vi.fn(),
  listModels: vi.fn(),
  getMe: vi.fn(),
  listBuiltinResources: vi.fn(),
  listAgentSkills: vi.fn(),
  listProfileMemories: vi.fn(),
}));

vi.mock("@/lib/api-client/sdk.gen", () => sdk);
vi.mock("@/lib/auth-users", () => ({ fetchAllAuthUsers: vi.fn(() => Promise.resolve([])) }));

const { loadAgentsSettingsData } = await import("./agent-settings");

beforeEach(() => {
  sdk.listAgents.mockReturnValue(ok({ agents: [{ id: "a1", name: "Ada" }] }));
  sdk.listModels.mockReturnValue(ok({ models: [] }));
  sdk.getMe.mockReturnValue(ok({ id: "u1", is_admin: false }));
  sdk.listBuiltinResources.mockReturnValue(ok({ resources: [] }));
  sdk.listAgentSkills.mockReturnValue(ok({ skills: [] }));
  sdk.listProfileMemories.mockReturnValue(
    ok({ memories: [{ agent_id: "a1", soul: "be kind", content: "prefers Go" }] }),
  );
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
});
