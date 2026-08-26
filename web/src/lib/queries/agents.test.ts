import { beforeEach, describe, expect, it, vi } from "vitest";
import { listAgents } from "@/lib/api-client/sdk.gen";
import { agentsQueryOptions, allAgentsAdminQueryOptions } from "./agents";

vi.mock("@/lib/api-client/sdk.gen", () => ({ listAgents: vi.fn() }));

beforeEach(() => {
  vi.mocked(listAgents).mockReset();
  // SAFETY: listAgents is mocked; the payload is the SDK-shaped agents response it resolves.
  vi.mocked(listAgents).mockResolvedValue({ data: { agents: [] } } as never);
});

describe("Agent list queries", () => {
  it("keeps the personal fleet query distinct and unexpanded", async () => {
    // SAFETY: the query's queryFn is invoked directly to assert its call shape.
    await (agentsQueryOptions.queryFn as () => Promise<unknown>)();

    expect(agentsQueryOptions.queryKey).toEqual(["agents"]);
    expect(listAgents).toHaveBeenCalledWith({ throwOnError: true });
  });

  it("requests the deployment-wide fleet only for the enabled admin picker query", async () => {
    const disabled = allAgentsAdminQueryOptions(false);
    const enabled = allAgentsAdminQueryOptions(true);
    // SAFETY: the query's queryFn is invoked directly to assert its call shape.
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
