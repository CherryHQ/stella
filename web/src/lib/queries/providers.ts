import { queryOptions } from "@tanstack/react-query";
import { listProviders, listProviderModels, listProviderTypes } from "@/lib/api-client";
import type { Provider, ProviderModel, ProviderType } from "@/lib/types";

export const providersQueryOptions = queryOptions({
  queryKey: ["providers"],
  queryFn: async () => {
    const { data } = await listProviders({ throwOnError: true });
    return (data?.providers ?? []) as Provider[];
  },
});

export const providerTypesQueryOptions = queryOptions({
  queryKey: ["provider-types"],
  queryFn: async () => {
    const { data } = await listProviderTypes({ throwOnError: true });
    return (data?.provider_types ?? []) as ProviderType[];
  },
});

export function providerModelsOptions(providerId: string) {
  return queryOptions({
    queryKey: ["provider-models", providerId],
    queryFn: async () => {
      const { data } = await listProviderModels({
        path: { id: providerId },
        throwOnError: true,
      });
      return (data?.models ?? []) as ProviderModel[];
    },
    enabled: !!providerId,
  });
}
