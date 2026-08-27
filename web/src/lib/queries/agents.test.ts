import { beforeEach, describe, expect, it, vi } from "vitest";
import * as sdk from "@/lib/api-client/sdk.gen";
import type { Agent } from "@/lib/types";
import { agentsQueryOptions, allAgentsAdminQueryOptions } from "./agents";

const listAgents = vi.spyOn(sdk, "listAgents");

beforeEach(() => {
  listAgents.mockReset();
  // SAFETY: listAgents is replaced with the SDK-shaped response used by this query test.
  listAgents.mockResolvedValue({ data: { agents: [] } } as never);
});

describe("Agent list queries", () => {
  it("keeps the personal fleet query distinct and unexpanded", async () => {
    // SAFETY: the query's queryFn is invoked directly to assert its call shape.
    await (agentsQueryOptions.queryFn as () => Promise<Agent[]>)();

    expect(agentsQueryOptions.queryKey).toEqual(["agents"]);
    expect(listAgents).toHaveBeenCalledWith({ throwOnError: true });
  });

  it("requests the deployment-wide fleet only for the enabled admin picker query", async () => {
    const disabled = allAgentsAdminQueryOptions(false);
    const enabled = allAgentsAdminQueryOptions(true);
    // SAFETY: the query's queryFn is invoked directly to assert its call shape.
    await (enabled.queryFn as () => Promise<Agent[]>)();

    expect(disabled.enabled).toBe(false);
    expect(enabled.enabled).toBe(true);
    expect(enabled.queryKey).toEqual(["agents", "admin", "all"]);
    expect(listAgents).toHaveBeenCalledWith({
      query: { include_all: true },
      throwOnError: true,
    });
  });
});
