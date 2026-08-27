import { queryOptions } from "@tanstack/react-query";
import { getOAuthProviderConfig, listOAuthProviders } from "@/lib/api-client/sdk.gen";
import type { OAuthProvider, OAuthProviderConfig } from "@/lib/types";

// The provider list already carries per-user connection state (connected,
// needs_reconnect, token expiries, granted/requested scopes), so the whole
// OAuth section reads from this one query — no per-provider status fan-out.
export const oauthProvidersQueryKey = ["oauth-providers"] as const;

export const oauthProvidersQueryOptions = queryOptions({
  queryKey: oauthProvidersQueryKey,
  queryFn: async () => {
    const { data } = await listOAuthProviders({ throwOnError: true });
    // SAFETY: listOAuthProviders returns provider items under data.providers.
    return (data?.providers ?? []) as OAuthProvider[];
  },
});

// Admin-only provider credentials + scope override. Keyed per provider and
// enabled lazily (only for the open sheet), so opening one provider does not
// fetch config for the rest.
export function oauthProviderConfigOptions(provider: string | null, enabled: boolean) {
  return queryOptions({
    queryKey: ["oauth-provider-config", provider],
    queryFn: async () => {
      const { data } = await getOAuthProviderConfig({
        // SAFETY: provider is a validated OAuth provider id used as the path id.
        path: { id: provider as string },
        throwOnError: true,
      });
      // SAFETY: getOAuthProviderConfig returns the config object, or null when absent.
      return (data ?? null) as OAuthProviderConfig | null;
    },
    enabled: enabled && !!provider,
    retry: false,
  });
}
