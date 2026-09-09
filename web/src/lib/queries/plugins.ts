import { queryOptions } from "@tanstack/react-query";
import { listNativePluginAgentDenials, listNativePlugins, listPlugins } from "@/lib/api-client";
import type { NativeAgentDeny, NativePlugin, PluginResource } from "@/lib/api-client";
import type { ScopeBand } from "@/lib/scope-band";

export type PluginScope = "system" | "system_agent" | "user" | "user_agent";
type PluginListQuery = { page_size: number; agent_id?: string; page_token?: string };

function nativePageQuery(pageToken?: string) {
  return pageToken ? { page_size: 500, page_token: pageToken } : { page_size: 500 };
}

async function fetchAllPlugins(agentId?: string): Promise<PluginResource[]> {
  const plugins: PluginResource[] = [];
  let pageToken: string | undefined;
  do {
    const query: PluginListQuery = {
      page_size: 500,
    };
    if (agentId) query.agent_id = agentId;
    if (pageToken) query.page_token = pageToken;
    const { data } = await listPlugins({
      query,
      throwOnError: true,
    });
    plugins.push(...(data?.plugins ?? []));
    pageToken = data?.next_page_token ?? undefined;
  } while (pageToken);
  return plugins;
}

export const pluginsQueryOptions = queryOptions({
  queryKey: ["plugins"],
  queryFn: () => fetchAllPlugins(),
});

export const scopedPluginsQueryOptions = (scopeBand: ScopeBand, agentId?: string) =>
  queryOptions({
    queryKey: ["plugins", scopeBand, agentId ?? null],
    queryFn: () => fetchAllPlugins(agentId),
  });

async function fetchAllNativePlugins(): Promise<NativePlugin[]> {
  const nativePlugins: NativePlugin[] = [];
  let pageToken: string | undefined;
  do {
    const { data } = await listNativePlugins({
      query: nativePageQuery(pageToken),
      throwOnError: true,
    });
    nativePlugins.push(...(data?.native_plugins ?? []));
    pageToken = data?.next_page_token ?? undefined;
  } while (pageToken);
  return nativePlugins;
}

export const nativePluginsQueryOptions = queryOptions({
  queryKey: ["native-plugins"],
  queryFn: fetchAllNativePlugins,
});

export const nativePluginDenialsQueryOptions = (nativeID: string, enabled: boolean) => {
  const slash = nativeID.indexOf("/");
  const kind = slash === -1 ? "" : nativeID.slice(0, slash);
  const name = slash === -1 ? nativeID : nativeID.slice(slash + 1);
  return queryOptions({
    queryKey: ["native-plugin-denials", nativeID],
    enabled: enabled && !!kind && !!name,
    queryFn: async () => {
      const denials: NativeAgentDeny[] = [];
      let pageToken: string | undefined;
      do {
        const { data } = await listNativePluginAgentDenials({
          path: { kind, name },
          query: nativePageQuery(pageToken),
          throwOnError: true,
        });
        denials.push(...(data?.denials ?? []));
        pageToken = data?.next_page_token ?? undefined;
      } while (pageToken);
      return denials;
    },
  });
};
