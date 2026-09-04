import { queryOptions } from "@tanstack/react-query";
import {
  listChannels,
  listFeishuChannelChats,
  listProfileIdentities,
  listPublicChannels,
} from "@/lib/api-client";
import type { Channel } from "@/lib/types";
import type {
  ComponentsIdentity,
  ComponentsPublicChannel,
  FeishuChat,
} from "@/lib/api-client/types.gen";

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

export function feishuChannelChatsQueryOptions(channelID: string, enabled: boolean) {
  return queryOptions({
    queryKey: ["feishu-channel-chats", channelID],
    enabled,
    queryFn: async (): Promise<FeishuChat[]> => {
      const chats: FeishuChat[] = [];
      let pageToken: string | undefined;
      do {
        const { data } = await listFeishuChannelChats({
          path: { id: channelID },
          query: { page_size: 100, page_token: pageToken },
          throwOnError: true,
        });
        chats.push(...(data?.chats ?? []));
        pageToken = data?.next_page_token ?? undefined;
      } while (pageToken);
      return chats;
    },
  });
}
