import { queryOptions } from "@tanstack/react-query";
import {
  listNativePluginAgentDenials,
  listNativePlugins,
  listPluginConfigs,
  listPlugins,
} from "@/lib/api-client";
import type {
  NativeAgentDeny,
  NativePlugin,
  PluginConfig,
  PluginDefinition,
} from "@/lib/api-client";

export type PluginScope = PluginConfig["scope"];

function nativePageQuery(pageToken?: string) {
  return pageToken ? { page_size: 500, page_token: pageToken } : { page_size: 500 };
}

async function fetchAllPlugins(): Promise<PluginDefinition[]> {
  const plugins: PluginDefinition[] = [];
  let pageToken: string | undefined;
  do {
    const { data } = await listPlugins({
      query: {
        page_size: 500,
        ...(pageToken ? { page_token: pageToken } : {}),
      },
      throwOnError: true,
    });
    plugins.push(...(data?.plugins ?? []));
    pageToken = data?.next_page_token ?? undefined;
  } while (pageToken);
  return plugins;
}

export const pluginsQueryOptions = queryOptions({
  queryKey: ["plugins"],
  queryFn: fetchAllPlugins,
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

export const pluginConfigsQueryOptions = (pluginID: string, scope: PluginScope, agentID?: string) =>
  queryOptions({
    queryKey: ["plugin-configs", pluginID, scope, agentID ?? null],
    // Agent-owned scopes require an explicit PEP target. A disabled query is
    // preferable to a broad request that could accidentally enumerate agents.
    enabled: scope === "user" || scope === "system" || !!agentID,
    queryFn: async () => {
      const configs: PluginConfig[] = [];
      let pageToken: string | undefined;
      do {
        const { data } = await listPluginConfigs({
          path: { plugin_id: pluginID },
          query: {
            scope,
            ...(agentID ? { agent_id: agentID } : {}),
            page_size: 500,
            ...(pageToken ? { page_token: pageToken } : {}),
          },
          throwOnError: true,
        });
        configs.push(...(data?.configs ?? []));
        pageToken = data?.next_page_token ?? undefined;
      } while (pageToken);
      return configs;
    },
  });
