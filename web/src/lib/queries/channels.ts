import { queryOptions } from "@tanstack/react-query";
import { listChannels, listPublicChannels, listProfileIdentities } from "@/lib/api-client";
import type { Channel } from "@/lib/types";
import type { ComponentsPublicChannel, ComponentsIdentity } from "@/lib/api-client/types.gen";

export const channelsQueryOptions = queryOptions({
  queryKey: ["channels"],
  queryFn: async () => {
    const { data } = await listChannels({ throwOnError: true });
    // SAFETY: listChannels returns channel items under data.channels.
    return (data?.channels ?? []) as Channel[];
  },
});

export const publicChannelsQueryOptions = queryOptions({
  queryKey: ["public-channels"],
  queryFn: async () => {
    const { data } = await listPublicChannels({ throwOnError: true });
    // SAFETY: listPublicChannels returns channel items under data.channels.
    return (data?.channels ?? []) as ComponentsPublicChannel[];
  },
});

export const profileIdentitiesQueryOptions = queryOptions({
  queryKey: ["profile-identities"],
  queryFn: async () => {
    const { data } = await listProfileIdentities({ throwOnError: true });
    // SAFETY: listProfileIdentities returns identity items under data.identities.
    return (data?.identities ?? []) as ComponentsIdentity[];
  },
});
