import { useState, useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { useI18n } from "@/lib/i18n";
import {
  listArticlesOptions,
  getDigestOptions,
  listStoredDigestsOptions,
  getStoredDigestOptions,
} from "@/lib/api-client/@tanstack/react-query.gen";
import type { ArticleStatus, SourceType } from "@/lib/api-client/types.gen";

export function useRecallyFilters() {
  const { t } = useI18n();
  const [searchText, setSearchText] = useState("");
  const [statusFilter, setStatusFilter] = useState<ArticleStatus | null>(null);
  const [sourceTypeFilter, setSourceTypeFilter] = useState<SourceType | null>(null);
  const [starredFilter, setStarredFilter] = useState<boolean | null>(null);
  const [tagFilter, setTagFilter] = useState<string | null>(null);
  const [showAllTags, setShowAllTags] = useState(false);
  const [leftOpen, setLeftOpen] = useState(true);
  const [digestView, setDigestView] = useState(false);
  const [selectedDigestDate, setSelectedDigestDate] = useState<string | null>(null);

  const digestQuery = useQuery(getDigestOptions());
  const storedDigestsQuery = useQuery(listStoredDigestsOptions({ query: { page_size: 50 } }));
  const selectedDigestQuery = useQuery({
    ...getStoredDigestOptions({ path: { date: selectedDigestDate ?? "" } }),
    enabled: !!selectedDigestDate,
  });
  const articlesQuery = useQuery(
    listArticlesOptions({
      query: {
        ...(searchText ? { q: searchText } : undefined),
        ...(statusFilter ? { status: statusFilter } : undefined),
        ...(sourceTypeFilter ? { source_type: sourceTypeFilter } : undefined),
        ...(starredFilter !== null ? { starred: starredFilter } : undefined),
        page_size: 50,
      },
    }),
  );

  const digest = digestQuery.data;
  const articles = articlesQuery.data?.articles ?? [];

  const displayArticles = useMemo(
    () => (tagFilter ? articles.filter((a) => a.tags?.includes(tagFilter)) : articles),
    [articles, tagFilter],
  );

  const tagData = useMemo(() => {
    const allTags = articles.flatMap((a) => a.tags ?? []);
    const tagCounts = allTags.reduce(
      (acc, tag) => {
        acc[tag] = (acc[tag] || 0) + 1;
        return acc;
      },
      // SAFETY: the reduce base is an empty string-keyed counter.
      {} as Record<string, number>,
    );
    const sortedTags = Object.entries(tagCounts)
      .sort((a, b) => b[1] - a[1])
      .map(([tag]) => tag);
    return {
      sortedTags,
      visibleTags: showAllTags ? sortedTags : sortedTags.slice(0, 10),
      hasMoreTags: sortedTags.length > 10,
      tagCounts,
    };
  }, [articles, showAllTags]);

  function clearFilters() {
    setStatusFilter(null);
    setStarredFilter(null);
    setSourceTypeFilter(null);
    setTagFilter(null);
    setDigestView(false);
  }

  return {
    t,
    searchText,
    setSearchText,
    statusFilter,
    setStatusFilter,
    sourceTypeFilter,
    setSourceTypeFilter,
    starredFilter,
    setStarredFilter,
    tagFilter,
    setTagFilter,
    showAllTags,
    setShowAllTags,
    leftOpen,
    setLeftOpen,
    digestView,
    setDigestView,
    digestQuery,
    digest,
    storedDigestsQuery,
    storedDigests: storedDigestsQuery.data?.digests ?? [],
    storedDigestsTotal: storedDigestsQuery.data?.total_size ?? 0,
    selectedDigestDate,
    setSelectedDigestDate,
    selectedDigestQuery,
    selectedDigest: selectedDigestQuery.data,
    articlesQuery,
    articles,
    displayArticles,
    ...tagData,
    clearFilters,
  };
}
