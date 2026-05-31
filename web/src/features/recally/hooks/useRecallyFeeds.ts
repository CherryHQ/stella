import { useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { useI18n } from "@/lib/i18n";
import {
  listFeedsOptions,
  listFeedsQueryKey,
  createFeedMutation,
  pollFeedMutation,
} from "@/lib/api-client/@tanstack/react-query.gen";
export function useRecallyFeeds(showToast: (message: string, type?: "success" | "error") => void) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const [feedUrl, setFeedUrl] = useState("");
  const [feedPollResults, setFeedPollResults] = useState<
    Record<string, { newCount: number; error?: string }>
  >({});

  const feedsQuery = useQuery(listFeedsOptions());
  const feeds = feedsQuery.data?.feeds ?? [];

  const createFeedMut = useMutation({
    ...createFeedMutation(),
    onSuccess: () => {
      setFeedUrl("");
      void queryClient.invalidateQueries({ queryKey: listFeedsQueryKey() });
      showToast(t("recally.feeds.added"));
    },
    onError: () => {
      showToast(t("recally.feeds.addFailed"), "error");
    },
  });

  const pollFeedMut = useMutation({
    ...pollFeedMutation(),
    onSuccess: (data) => {
      setFeedPollResults((prev) => ({
        ...prev,
        [data.feed.id]: {
          newCount: data.new_entries.length,
          error: data.error ?? undefined,
        },
      }));
      void queryClient.invalidateQueries({ queryKey: listFeedsQueryKey() });
      if (data.error) {
        showToast(`${t("recally.feeds.pollError")}: ${data.error}`, "error");
      } else if (data.new_entries.length > 0) {
        showToast(t("recally.feeds.pollNewEntries", { count: data.new_entries.length }));
      }
    },
    onError: () => {
      showToast(t("recally.feeds.pollFailed"), "error");
    },
  });

  return {
    t,
    feedUrl,
    setFeedUrl,
    feedPollResults,
    setFeedPollResults,
    feedsQuery,
    feeds,
    createFeedMut,
    pollFeedMut,
  };
}
