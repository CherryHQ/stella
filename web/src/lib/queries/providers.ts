import { queryOptions } from "@tanstack/react-query";
import {
  getModelCatalogStatus,
  listModelCatalogModels,
  listModelCatalogProviders,
  listProviders,
  listProviderModels,
  listProviderTypes,
} from "@/lib/api-client";
import type {
  CatalogModelReference,
  CatalogProvider,
  ModelCatalogStatus,
} from "@/lib/api-client/types.gen";
import type { Provider, ProviderModel, ProviderType } from "@/lib/types";

export const providersQueryOptions = queryOptions({
  queryKey: ["providers"],
  queryFn: async () => {
    const { data } = await listProviders({ throwOnError: true });
    // SAFETY: listProviders returns provider items under data.providers.
    return (data?.providers ?? []) as Provider[];
  },
});

export const providerTypesQueryOptions = queryOptions({
  queryKey: ["provider-types"],
  queryFn: async () => {
    const { data } = await listProviderTypes({ throwOnError: true });
    // SAFETY: listProviderTypes returns provider-type items under data.provider_types.
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
      // SAFETY: listProviderModels returns model items under data.models.
      return (data?.models ?? []) as ProviderModel[];
    },
    enabled: !!providerId,
  });
}

export const modelCatalogProvidersOptions = queryOptions({
  queryKey: ["model-catalog", "providers"],
  queryFn: async () => {
    const { data } = await listModelCatalogProviders({ throwOnError: true });
    // SAFETY: the generated catalog-list response owns a CatalogProvider array.
    return (data?.providers ?? []) as CatalogProvider[];
  },
  staleTime: 5 * 60 * 1000,
});

export const modelCatalogModelsOptions = queryOptions({
  queryKey: ["model-catalog", "models"],
  queryFn: async () => {
    const { data } = await listModelCatalogModels({ throwOnError: true });
    // SAFETY: the generated catalog-model response owns a CatalogModelReference array.
    return (data?.models ?? []) as CatalogModelReference[];
  },
  staleTime: 5 * 60 * 1000,
});

export const modelCatalogStatusOptions = queryOptions({
  queryKey: ["model-catalog", "status"],
  queryFn: async () => {
    const { data } = await getModelCatalogStatus({ throwOnError: true });
    // SAFETY: the generated status endpoint returns ModelCatalogStatus directly.
    return data as ModelCatalogStatus;
  },
  staleTime: 60 * 1000,
});
