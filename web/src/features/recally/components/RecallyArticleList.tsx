import { useState, type Dispatch, type SetStateAction } from "react";

import {
  SlidersHorizontal,
  Search,
  RefreshCw,
  Plus,
  Inbox,
  Star,
  Archive,
  History,
  ChevronDown,
  Tag,
} from "lucide-react";
import { useSidebar } from "@/components/ui/sidebar";
import { Popover, PopoverTrigger, PopoverContent } from "@/components/ui/popover";
import type {
  Article,
  ArticleStatus,
  SourceType,
  Feed,
  StoredDigestSummary,
} from "@/lib/api-client/types.gen";
import type { TFunction } from "../constants";
import { ArticleCard } from "./ArticleCard";
import { FilterChip } from "./FilterChip";
import { SOURCE_TYPES, SOURCE_LABEL_KEYS } from "../constants";
import { cn } from "@/lib/utils";

export function RecallyArticleList({
  t,
  displayArticles,
  articlesQuery,
  selectedId,
  setSelectedId,

  // Filters State
  searchText,
  setSearchText,
  statusFilter,
  setStatusFilter,
  starredFilter,
  setStarredFilter,
  sourceTypeFilter,
  setSourceTypeFilter,
  tagFilter,
  setTagFilter,

  // Digest View Integration
  digest,
  digestView,
  setDigestView,
  storedDigests,
  storedDigestsLoading,
  selectedDigestDate,
  onSelectDigest,
  clearFilters,

  // Tags
  sortedTags,
  visibleTags,
  showAllTags,
  setShowAllTags,
  tagCounts,

  // Feeds
  feeds,
  feedUrl,
  setFeedUrl,
  createFeedMut,
  pollFeedMut,
  feedPollResults,
}: {
  t: TFunction;
  displayArticles: Article[];
  articlesQuery: { isLoading: boolean; isError: boolean };
  selectedId: string | null;
  setSelectedId: (id: string | null) => void;

  searchText: string;
  setSearchText: Dispatch<SetStateAction<string>>;
  statusFilter: ArticleStatus | null;
  setStatusFilter: Dispatch<SetStateAction<ArticleStatus | null>>;
  starredFilter: boolean | null;
  setStarredFilter: Dispatch<SetStateAction<boolean | null>>;
  sourceTypeFilter: SourceType | null;
  setSourceTypeFilter: Dispatch<SetStateAction<SourceType | null>>;
  tagFilter: string | null;
  setTagFilter: Dispatch<SetStateAction<string | null>>;

  digest:
    | {
        total_articles?: number;
        unread_count?: number;
        starred_count?: number;
        archived_count?: number;
        saved_yesterday_count?: number;
        worth_revisiting_count?: number;
      }
    | undefined;
  digestView: boolean;
  setDigestView: Dispatch<SetStateAction<boolean>>;
  storedDigests: StoredDigestSummary[];
  storedDigestsLoading: boolean;
  selectedDigestDate: string | null;
  onSelectDigest: (date: string) => void;
  clearFilters: () => void;

  sortedTags: string[];
  visibleTags: string[];
  showAllTags: boolean;
  setShowAllTags: Dispatch<SetStateAction<boolean>>;
  tagCounts: Record<string, number>;

  feeds: Feed[];
  feedUrl: string;
  setFeedUrl: Dispatch<SetStateAction<string>>;
  createFeedMut: { isPending: boolean; mutate: (args: { body: { url: string } }) => void };
  pollFeedMut: {
    isPending: boolean;
    mutate: (args: { path: { id: string } }) => void;
    variables?: { path: { id: string } };
  };
  feedPollResults: Record<string, { newCount: number; error?: string }>;
}) {
  const [explorerOpen, setExplorerOpen] = useState(false);
  const [refinementsOpen, setRefinementsOpen] = useState(false);
  const hasMoreTags = sortedTags.length > 10;

  // Active filter checks for badge count
  const activeFiltersCount = (statusFilter !== null ? 1 : 0) + (sourceTypeFilter !== null ? 1 : 0);

  // Determine active source icon and title
  let ActiveSourceIcon = Inbox;
  let activeSourceName = t("recally.nav.inbox");
  let activeSourceCount = digest?.total_articles ?? 0;

  if (digestView) {
    ActiveSourceIcon = History;
    activeSourceName = t("recally.nav.digest");
    activeSourceCount = storedDigests.length;
  } else if (starredFilter) {
    ActiveSourceIcon = Star;
    activeSourceName = t("recally.nav.starred");
    activeSourceCount = digest?.starred_count ?? 0;
  } else if (statusFilter === "archived") {
    ActiveSourceIcon = Archive;
    activeSourceName = t("recally.nav.archive");
    activeSourceCount = digest?.archived_count ?? 0;
  } else if (tagFilter) {
    ActiveSourceIcon = Tag;
    activeSourceName = tagFilter;
    activeSourceCount = tagCounts[tagFilter] ?? 0;
  }

  // Active source selection logic
  const selectInbox = () => {
    setDigestView(false);
    clearFilters();
    setExplorerOpen(false);
  };

  const selectStarred = () => {
    setDigestView(false);
    setStarredFilter(true);
    setStatusFilter(null);
    setSourceTypeFilter(null);
    setTagFilter(null);
    setExplorerOpen(false);
  };

  const selectArchive = () => {
    setDigestView(false);
    setStatusFilter("archived");
    setStarredFilter(null);
    setSourceTypeFilter(null);
    setTagFilter(null);
    setExplorerOpen(false);
  };

  const selectDigest = () => {
    setDigestView(true);
    setStatusFilter(null);
    setStarredFilter(null);
    setSourceTypeFilter(null);
    setTagFilter(null);
    setExplorerOpen(false);
  };

  const selectTag = (tag: string | null) => {
    setTagFilter(tag);
    setDigestView(false);
    setStarredFilter(null);
    setStatusFilter(null);
    setSourceTypeFilter(null);
    setExplorerOpen(false);
  };

  const handleTagClick = (tag: string) => {
    if (tagFilter === tag) {
      selectTag(null);
    } else {
      selectTag(tag);
    }
  };

  const { setOpenMobile } = useSidebar();

  const handleSelectArticle = (id: string) => {
    setSelectedId(id);
    setOpenMobile(false);
  };

  const handleDigestSelect = (date: string) => {
    onSelectDigest(date);
    setOpenMobile(false);
  };

  return (
    <div className="flex min-h-0 w-full flex-col overflow-hidden">
      {/* Source Selector + Search + Filters Toolbar */}
      <div className="shrink-0 border-b border-border bg-card px-3 py-2">
        <div className="flex items-center gap-2">
          <Popover open={explorerOpen} onOpenChange={setExplorerOpen}>
            <PopoverTrigger
              className={cn(
                "flex items-center gap-2 px-3 py-1.5 rounded-xl border transition-colors duration-120 cursor-pointer text-xs font-semibold select-none outline-none",
                explorerOpen
                  ? "bg-primary/10 text-primary border-primary/25"
                  : "bg-card border-border text-foreground hover:bg-muted",
              )}
            >
              <ActiveSourceIcon
                className={cn("size-3.5", explorerOpen ? "text-primary" : "text-muted-foreground")}
              />
              <span className="truncate max-w-[120px] font-mono tracking-tight">
                {activeSourceName}
              </span>
              <span className="font-mono text-[9px] px-1 py-0.5 rounded-md bg-muted/65 text-muted-foreground font-medium scale-90">
                {activeSourceCount}
              </span>
              <ChevronDown
                className={cn(
                  "size-3 text-muted-foreground transition-transform duration-120 ml-0.5",
                  explorerOpen && "rotate-180 text-primary",
                )}
              />
            </PopoverTrigger>

            <PopoverContent
              align="start"
              sideOffset={8}
              className="w-[calc(100vw-2rem)] max-w-[560px] p-4 bg-popover border border-border rounded-xl"
            >
              <div className="grid grid-cols-1 gap-4 sm:grid-cols-3 sm:divide-x sm:divide-border/30">
                {/* Library / Folders */}
                <div className="space-y-2">
                  <div className="text-[10px] font-mono font-semibold text-muted-foreground px-1">
                    {t("recally.section.library")}
                  </div>
                  <div className="space-y-1">
                    <button
                      onClick={selectInbox}
                      className={cn(
                        "flex items-center justify-between w-full px-3 py-1.5 rounded-xl text-xs transition-all cursor-pointer font-medium",
                        !digestView &&
                          statusFilter === null &&
                          starredFilter === null &&
                          tagFilter === null
                          ? "bg-primary/10 text-primary font-semibold"
                          : "text-muted-foreground hover:bg-muted/40 hover:text-foreground",
                      )}
                    >
                      <div className="flex items-center gap-2">
                        <Inbox className="size-3.5" />
                        <span>{t("recally.nav.inbox")}</span>
                      </div>
                      <span className="font-mono text-[9px] bg-muted px-1.5 py-0.5 rounded-md text-muted-foreground">
                        {digest?.total_articles ?? 0}
                      </span>
                    </button>
                    <button
                      onClick={selectStarred}
                      className={cn(
                        "flex items-center justify-between w-full px-3 py-1.5 rounded-xl text-xs transition-all cursor-pointer font-medium",
                        !digestView && starredFilter === true && tagFilter === null
                          ? "bg-primary/10 text-primary font-semibold"
                          : "text-muted-foreground hover:bg-muted/40 hover:text-foreground",
                      )}
                    >
                      <div className="flex items-center gap-2">
                        <Star className="size-3.5" />
                        <span>{t("recally.nav.starred")}</span>
                      </div>
                      <span className="font-mono text-[9px] bg-muted px-1.5 py-0.5 rounded-md text-muted-foreground">
                        {digest?.starred_count ?? 0}
                      </span>
                    </button>
                    <button
                      onClick={selectArchive}
                      className={cn(
                        "flex items-center justify-between w-full px-3 py-1.5 rounded-xl text-xs transition-all cursor-pointer font-medium",
                        !digestView && statusFilter === "archived" && tagFilter === null
                          ? "bg-primary/10 text-primary font-semibold"
                          : "text-muted-foreground hover:bg-muted/40 hover:text-foreground",
                      )}
                    >
                      <div className="flex items-center gap-2">
                        <Archive className="size-3.5" />
                        <span>{t("recally.nav.archive")}</span>
                      </div>
                      <span className="font-mono text-[9px] bg-muted px-1.5 py-0.5 rounded-md text-muted-foreground">
                        {digest?.archived_count ?? 0}
                      </span>
                    </button>
                    <button
                      onClick={selectDigest}
                      className={cn(
                        "flex items-center justify-between w-full px-3 py-1.5 rounded-xl text-xs transition-all cursor-pointer font-medium",
                        digestView
                          ? "bg-primary/10 text-primary font-semibold"
                          : "text-muted-foreground hover:bg-muted/40 hover:text-foreground",
                      )}
                    >
                      <div className="flex items-center gap-2">
                        <History className="size-3.5" />
                        <span>{t("recally.nav.digest")}</span>
                      </div>
                      <span className="font-mono text-[9px] bg-muted px-1.5 py-0.5 rounded-md text-muted-foreground">
                        {digest?.saved_yesterday_count ?? 0}
                      </span>
                    </button>
                  </div>
                </div>

                {/* Subscriptions */}
                <div className="space-y-2 sm:px-4 flex flex-col min-h-0">
                  <div className="text-[10px] font-mono font-semibold text-muted-foreground px-1">
                    {t("recally.section.feeds")}
                  </div>
                  <div className="flex items-center gap-1.5 px-1">
                    <input
                      type="text"
                      value={feedUrl}
                      onChange={(e) => setFeedUrl(e.target.value)}
                      placeholder={t("recally.feeds.addFeedPlaceholder")}
                      onKeyDown={(e) => {
                        if (e.key === "Enter" && feedUrl.trim()) {
                          createFeedMut.mutate({ body: { url: feedUrl.trim() } });
                        }
                      }}
                      className="h-7 flex-1 rounded-lg border border-border/40 px-2.5 text-[10px] font-mono placeholder:text-muted-foreground/45 hover:border-border/60 focus:border-primary/40 focus:ring-1 focus:ring-primary/10 focus:outline-none transition-all duration-150 text-foreground"
                    />
                    <button
                      onClick={() => {
                        if (feedUrl.trim()) {
                          createFeedMut.mutate({ body: { url: feedUrl.trim() } });
                        }
                      }}
                      disabled={createFeedMut.isPending || !feedUrl.trim()}
                      className="inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-lg border border-border/40 hover:bg-muted transition-colors disabled:opacity-50 cursor-pointer"
                    >
                      {createFeedMut.isPending ? (
                        <RefreshCw className="size-3 animate-spin text-muted-foreground" />
                      ) : (
                        <Plus className="size-3 text-muted-foreground" />
                      )}
                    </button>
                  </div>
                  {feeds.length > 0 ? (
                    <div className="flex-1 max-h-36 overflow-y-auto space-y-1 pr-1 border border-border/15 rounded-lg p-1.5 bg-card/35 scrollbar-thin mt-1">
                      {feeds.map((feed: Feed) => (
                        <div
                          key={feed.id}
                          className="flex items-center justify-between gap-1.5 py-0.5 border-b border-border/10 last:border-0"
                        >
                          <span
                            className="truncate text-[10px] text-foreground font-medium"
                            title={feed.title || feed.url}
                          >
                            {feed.title || feed.url}
                          </span>
                          <button
                            onClick={() => pollFeedMut.mutate({ path: { id: feed.id } })}
                            disabled={pollFeedMut.isPending}
                            className="inline-flex shrink-0 items-center gap-1 rounded-md border border-border/30 px-1 py-0.5 font-mono text-[8px] text-muted-foreground hover:text-foreground transition-colors disabled:opacity-50 cursor-pointer"
                          >
                            {pollFeedMut.isPending && pollFeedMut.variables?.path.id === feed.id ? (
                              <RefreshCw className="size-2.5 animate-spin" />
                            ) : feedPollResults[feed.id]?.error ? (
                              <span className="text-destructive font-semibold">Err</span>
                            ) : feedPollResults[feed.id] ? (
                              <span className="text-primary font-semibold">
                                +{feedPollResults[feed.id].newCount}
                              </span>
                            ) : (
                              <>
                                <RefreshCw className="size-2.5" />
                                <span>Poll</span>
                              </>
                            )}
                          </button>
                        </div>
                      ))}
                    </div>
                  ) : (
                    <div className="text-[10px] font-mono text-muted-foreground/45 text-center py-4">
                      {t("recally.feeds.noFeedsDesc")}
                    </div>
                  )}
                </div>

                {/* Tags */}
                <div className="space-y-2 sm:pl-4">
                  <div className="text-[10px] font-mono font-semibold text-muted-foreground px-1">
                    {t("recally.section.tags")}
                  </div>
                  {sortedTags.length > 0 ? (
                    <div className="flex flex-wrap gap-1 max-h-[180px] overflow-y-auto pr-1">
                      {visibleTags.map((tag) => (
                        <button
                          key={tag}
                          onClick={() => handleTagClick(tag)}
                          className={cn(
                            "rounded-full px-2 py-0.5 text-[10px] font-mono border transition-all cursor-pointer whitespace-nowrap",
                            tagFilter === tag
                              ? "bg-primary/10 text-primary border-primary/20 font-semibold"
                              : "bg-muted/40 border-border/20 text-muted-foreground hover:text-foreground",
                          )}
                        >
                          {tag} ({tagCounts[tag]})
                        </button>
                      ))}
                      {hasMoreTags && (
                        <button
                          onClick={() => setShowAllTags(!showAllTags)}
                          className="rounded-full bg-muted/40 border border-border/20 px-2 py-0.5 text-[10px] font-mono text-muted-foreground transition-all hover:text-foreground cursor-pointer"
                        >
                          {showAllTags
                            ? t("recally.tags.less")
                            : t("recally.tags.more", { count: sortedTags.length - 10 })}
                        </button>
                      )}
                    </div>
                  ) : (
                    <div className="text-[10px] font-mono text-muted-foreground/45 text-center py-4">
                      No tags
                    </div>
                  )}
                </div>
              </div>
            </PopoverContent>
          </Popover>

          {/* Search box */}
          {!digestView && (
            <div className="relative flex-1">
              <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 w-3.5 h-3.5 text-muted-foreground/45 pointer-events-none" />
              <input
                type="text"
                value={searchText}
                onChange={(e) => setSearchText(e.target.value)}
                placeholder={t("recally.searchPlaceholder")}
                className="w-full pl-8 pr-3 py-1.5 text-[11px] font-mono rounded-xl border border-border focus:border-primary focus:ring-1 focus:ring-primary focus:outline-none transition-colors duration-120 text-foreground placeholder:text-muted-foreground/45"
              />
            </div>
          )}

          {/* Live refinement filter toggle button */}
          {!digestView && (
            <button
              onClick={() => setRefinementsOpen(!refinementsOpen)}
              className={cn(
                "relative p-2 rounded-xl border transition-colors duration-120 cursor-pointer flex items-center justify-center shrink-0",
                refinementsOpen || activeFiltersCount > 0
                  ? "bg-primary/10 text-primary border-primary/25"
                  : "bg-card border-border text-muted-foreground hover:text-foreground hover:bg-muted",
              )}
              title="Filter refinements"
            >
              <SlidersHorizontal className="size-3.5" />
              {activeFiltersCount > 0 && (
                <span className="absolute -top-1 -right-1 flex h-4 w-4 items-center justify-center rounded-full bg-primary text-[8px] font-bold text-primary-foreground">
                  {activeFiltersCount}
                </span>
              )}
            </button>
          )}
        </div>
      </div>

      {/* Inline Quick Refinements Toolbar */}
      {refinementsOpen && !digestView && (
        <div className="shrink-0 border-b border-border/40 p-3 space-y-2.5">
          {/* Status Filters */}
          <div className="flex items-center gap-3">
            <span className="text-[10px] font-mono font-semibold text-muted-foreground w-12 shrink-0">
              Status:
            </span>
            <div className="flex flex-wrap gap-1.5">
              <FilterChip
                label={t("recally.status.all")}
                active={statusFilter === null}
                onClick={() => setStatusFilter(null)}
              />
              <FilterChip
                label={t("recally.status.unread")}
                active={statusFilter === "unread"}
                onClick={() => setStatusFilter("unread")}
              />
              <FilterChip
                label={t("recally.status.read")}
                active={statusFilter === "read"}
                onClick={() => setStatusFilter("read")}
              />
            </div>
          </div>

          {/* Source Type Filters */}
          <div className="flex items-center gap-3">
            <span className="text-[10px] font-mono font-semibold text-muted-foreground w-12 shrink-0">
              Source:
            </span>
            <div className="flex flex-wrap gap-1.5">
              <FilterChip
                label={t("recally.status.all")}
                active={sourceTypeFilter === null}
                onClick={() => setSourceTypeFilter(null)}
              />
              {SOURCE_TYPES.map((type) => (
                <FilterChip
                  key={type}
                  label={t(SOURCE_LABEL_KEYS[type])}
                  active={sourceTypeFilter === type}
                  onClick={() => setSourceTypeFilter(sourceTypeFilter === type ? null : type)}
                />
              ))}
            </div>
          </div>
        </div>
      )}

      {/* List Area */}
      <div className="flex-1 space-y-2 overflow-auto p-3">
        {digestView ? (
          <>
            {storedDigestsLoading && (
              <div className="flex items-center justify-center h-32">
                <div className="w-3.5 h-3.5 border border-muted-foreground/30 border-t-muted-foreground rounded-full animate-spin" />
              </div>
            )}
            {!storedDigestsLoading && storedDigests.length === 0 && (
              <div className="flex flex-col items-center justify-center h-32 text-center">
                <p className="text-xs font-mono text-muted-foreground">
                  {t("recally.digest.noHistory")}
                </p>
              </div>
            )}
            {storedDigests.map((d) => (
              <button
                key={d.id}
                type="button"
                onClick={() => handleDigestSelect(d.date)}
                className={cn(
                  "w-full rounded-xl border p-3.5 text-left transition-colors duration-120 cursor-pointer",
                  selectedDigestDate === d.date
                    ? "border-primary/30 bg-primary/5 text-foreground ring-1 ring-primary/20"
                    : "border-border bg-card hover:border-border/80 hover:bg-muted",
                )}
              >
                <div className="font-mono text-sm font-semibold text-foreground">{d.date}</div>
                <div className="mt-1.5 text-xs text-muted-foreground">
                  {d.saved_yesterday_count} {t("recally.stat.savedYesterday")} ·{" "}
                  {d.worth_revisiting_count} {t("recally.stat.worthRevisiting")}
                </div>
              </button>
            ))}
          </>
        ) : (
          <>
            {articlesQuery.isLoading && (
              <div className="flex items-center justify-center h-32">
                <div className="w-3.5 h-3.5 border border-muted-foreground/30 border-t-muted-foreground/75 rounded-full animate-spin" />
              </div>
            )}
            {articlesQuery.isError && (
              <div className="flex items-center justify-center h-32 text-xs font-mono text-destructive">
                {t("common.error")}
              </div>
            )}
            {!articlesQuery.isLoading && !articlesQuery.isError && displayArticles.length === 0 && (
              <div className="flex flex-col items-center justify-center h-32 text-center">
                <p className="text-xs font-mono text-muted-foreground">
                  {t("recally.empty.noArticles")}
                </p>
                <p className="text-[10px] font-mono text-muted-foreground/45 mt-1">
                  {t("recally.empty.noArticlesDesc")}
                </p>
              </div>
            )}
            {displayArticles.map((article) => (
              <ArticleCard
                key={article.id}
                article={article}
                selected={selectedId === article.id}
                onClick={() => handleSelectArticle(article.id)}
                t={t}
              />
            ))}
          </>
        )}
      </div>
    </div>
  );
}
