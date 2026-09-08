import { beforeEach, describe, expect, it, vi } from "vitest";
import * as sdk from "@/lib/api-client/sdk.gen";
import {
  nativePluginDenialsQueryOptions,
  nativePluginsQueryOptions,
  scopedPluginsQueryOptions,
} from "./plugins";

const listNativePlugins = vi.spyOn(sdk, "listNativePlugins");
const listNativePluginAgentDenials = vi.spyOn(sdk, "listNativePluginAgentDenials");
const listPlugins = vi.spyOn(sdk, "listPlugins");

function sdkResponse<T>(data: T) {
  // SAFETY: tests provide the exact response shape consumed by the generated SDK wrapper.
  return Promise.resolve({ data }) as never;
}

beforeEach(() => {
  listNativePlugins.mockReset();
  listNativePluginAgentDenials.mockReset();
  listPlugins.mockReset();
});

describe("native capability queries", () => {
  it("walks native plugin pagination", async () => {
    listNativePlugins
      .mockResolvedValueOnce(
        sdkResponse({
          native_plugins: [{ id: "system/email", is_enabled: true }],
          next_page_token: "next",
        }),
      )
      .mockResolvedValueOnce(
        sdkResponse({
          native_plugins: [{ id: "system/recally", is_enabled: false }],
        }),
      );

    // SAFETY: this query's generated SDK function is replaced with the paged fixture above.
    const plugins = await (nativePluginsQueryOptions.queryFn as () => Promise<unknown[]>)();
    expect(plugins).toEqual([
      { id: "system/email", is_enabled: true },
      { id: "system/recally", is_enabled: false },
    ]);
    expect(listNativePlugins).toHaveBeenNthCalledWith(1, {
      query: { page_size: 500 },
      throwOnError: true,
    });
    expect(listNativePlugins).toHaveBeenNthCalledWith(2, {
      query: { page_size: 500, page_token: "next" },
      throwOnError: true,
    });
  });

  it("walks Agent deny pagination and stays disabled without a native selection", async () => {
    const disabled = nativePluginDenialsQueryOptions("", false);
    expect(disabled.enabled).toBe(false);

    listNativePluginAgentDenials
      .mockResolvedValueOnce(
        sdkResponse({
          denials: [{ native_id: "system/email", agent_id: "agent-a", is_denied: true }],
          next_page_token: "next",
        }),
      )
      .mockResolvedValueOnce(
        sdkResponse({
          denials: [{ native_id: "system/email", agent_id: "agent-b", is_denied: true }],
        }),
      );
    const options = nativePluginDenialsQueryOptions("system/email", true);
    // SAFETY: queryFn is invoked directly with no QueryObserver context in this pagination test.
    const denials = await (options.queryFn as () => Promise<unknown[]>)();
    expect(denials).toHaveLength(2);
    expect(listNativePluginAgentDenials).toHaveBeenNthCalledWith(1, {
      path: { kind: "system", name: "email" },
      query: { page_size: 500 },
      throwOnError: true,
    });
  });

  it("walks raw plugin resource pagination for an agent", async () => {
    listPlugins
      .mockResolvedValueOnce(
        sdkResponse({
          plugins: [{ id: "plugin-a", scope: "user_agent" }],
          next_page_token: "next",
        }),
      )
      .mockResolvedValueOnce(sdkResponse({ plugins: [{ id: "plugin-b", scope: "user" }] }));
    const options = scopedPluginsQueryOptions("personal", "agent-a");
    const plugins = await (options.queryFn as () => Promise<unknown[]>)();
    expect(plugins).toEqual([
      { id: "plugin-a", scope: "user_agent" },
      { id: "plugin-b", scope: "user" },
    ]);
    expect(listPlugins).toHaveBeenNthCalledWith(1, {
      query: { page_size: 500, agent_id: "agent-a" },
      throwOnError: true,
    });
    expect(listPlugins).toHaveBeenNthCalledWith(2, {
      query: { page_size: 500, agent_id: "agent-a", page_token: "next" },
      throwOnError: true,
    });
  });
});
