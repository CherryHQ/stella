import { beforeEach, describe, expect, it, vi } from "vitest";
import { listAgents } from "@/lib/api-client/sdk.gen";
import { agentsQueryOptions, allAgentsAdminQueryOptions } from "./agents";

vi.mock("@/lib/api-client/sdk.gen", () => ({ listAgents: vi.fn() }));

beforeEach(() => {
  vi.mocked(listAgents).mockReset();
  vi.mocked(listAgents).mockResolvedValue({ data: { agents: [] } } as never);
});

describe("Agent list queries", () => {
  it("keeps the personal fleet query distinct and unexpanded", async () => {
    await (agentsQueryOptions.queryFn as () => Promise<unknown>)();

    expect(agentsQueryOptions.queryKey).toEqual(["agents"]);
    expect(listAgents).toHaveBeenCalledWith({ throwOnError: true });
  });

  it("requests the deployment-wide fleet only for the enabled admin picker query", async () => {
    const disabled = allAgentsAdminQueryOptions(false);
    const enabled = allAgentsAdminQueryOptions(true);
    await (enabled.queryFn as () => Promise<unknown>)();

    expect(disabled.enabled).toBe(false);
    expect(enabled.enabled).toBe(true);
    expect(enabled.queryKey).toEqual(["agents", "admin", "all"]);
    expect(listAgents).toHaveBeenCalledWith({
      query: { include_all: true },
      throwOnError: true,
    });
  });
});
