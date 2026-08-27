import { queryOptions } from "@tanstack/react-query";
import { listPlugins, listManifestPlugins } from "@/lib/api-client";
import type { Plugin } from "@/lib/types";

export const pluginsQueryOptions = queryOptions({
  queryKey: ["plugins"],
  queryFn: async () => {
    const { data } = await listPlugins({ throwOnError: true });
    // SAFETY: listPlugins returns plugin items under data.plugins.
    return (data?.plugins ?? []) as Plugin[];
  },
});

export const manifestPluginsQueryOptions = queryOptions({
  queryKey: ["manifest-plugins"],
  queryFn: async () => {
    const { data } = await listManifestPlugins({ throwOnError: true });
    return data;
  },
});
