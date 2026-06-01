import { useState, type Dispatch, type SetStateAction } from "react";
import {
  SlidersHorizontal,
  Search,
  RefreshCw,
  Plus,
  AlertCircle,
  Inbox,
  Star,
  Archive,
  History,
  PanelLeftOpen,
  PanelLeftClose,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { useSidebar } from "@/components/ui/sidebar";
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
import { StatCard } from "./StatCard";
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
  const { state: sidebarState, toggleSidebar } = useSidebar();
  const [showFiltersPanel, setShowFiltersPanel] = useState(false);
  const hasMoreTags = sortedTags.length > 10;

  // Active filter checks for badge count
  const activeFiltersCount =
    (statusFilter !== null ? 1 : 0) +
    (sourceTypeFilter !== null ? 1 : 0) +
    (tagFilter !== null ? 1 : 0);

  return (
    <section className="flex min-h-0 flex-1 flex-col overflow-hidden border-r border-border bg-card/40">
      {/* Segmented Control / Tabs */}
      <div className="shrink-0 border-b border-border bg-card/65 px-4 pt-3 pb-2 backdrop-blur-xl">
        <div className="flex items-center justify-between mb-3">
          <div className="flex items-center gap-2">
            <Button
              variant="ghost"
              size="xs"
              onClick={toggleSidebar}
              className="hidden h-7 w-7 shrink-0 rounded-full p-0 text-muted-foreground md:inline-flex cursor-pointer"
              title={sidebarState === "collapsed" ? "Show sidebar" : "Hide sidebar"}
            >
              {sidebarState === "collapsed" ? (
                <PanelLeftOpen className="size-3.5" />
              ) : (
                <PanelLeftClose className="size-3.5" />
              )}
            </Button>
            <h1 className="text-[13px] font-semibold tracking-tight text-foreground/90 font-mono">
              {t("recally.title")}
            </h1>
          </div>
          <span className="font-mono text-[10px] text-muted-foreground/45 tabular-nums">
            {digestView ? storedDigests.length : displayArticles.length} entries
          </span>
        </div>

        <div className="grid grid-cols-4 gap-1 p-0.5 bg-muted/40 rounded-xl border border-border/30">
          <button
            onClick={() => {
              setDigestView(false);
              clearFilters();
            }}
            className={cn(
              "flex flex-col items-center justify-center py-1.5 rounded-lg text-[10px] font-medium transition-all cursor-pointer",
              !digestView && statusFilter === null && starredFilter === null && tagFilter === null
                ? "bg-background text-primary shadow-2xs font-semibold"
                : "text-muted-foreground hover:text-foreground",
            )}
          >
            <Inbox className="size-3.5 mb-0.5" />
            <span>{t("recally.nav.inbox")}</span>
          </button>
          <button
            onClick={() => {
              setDigestView(false);
              setStarredFilter(true);
              setStatusFilter(null);
              setSourceTypeFilter(null);
              setTagFilter(null);
            }}
            className={cn(
              "flex flex-col items-center justify-center py-1.5 rounded-lg text-[10px] font-medium transition-all cursor-pointer",
              !digestView && starredFilter === true && tagFilter === null
                ? "bg-background text-primary shadow-2xs font-semibold"
                : "text-muted-foreground hover:text-foreground",
            )}
          >
            <Star className="size-3.5 mb-0.5" />
            <span>{t("recally.nav.starred")}</span>
          </button>
          <button
            onClick={() => {
              setDigestView(false);
              setStatusFilter("archived");
              setStarredFilter(null);
              setSourceTypeFilter(null);
              setTagFilter(null);
            }}
            className={cn(
              "flex flex-col items-center justify-center py-1.5 rounded-lg text-[10px] font-medium transition-all cursor-pointer",
              !digestView && statusFilter === "archived" && tagFilter === null
                ? "bg-background text-primary shadow-2xs font-semibold"
                : "text-muted-foreground hover:text-foreground",
            )}
          >
            <Archive className="size-3.5 mb-0.5" />
            <span>{t("recally.nav.archive")}</span>
          </button>
          <button
            onClick={() => {
              setDigestView(true);
              setStatusFilter(null);
              setStarredFilter(null);
              setSourceTypeFilter(null);
              setTagFilter(null);
            }}
            className={cn(
              "flex flex-col items-center justify-center py-1.5 rounded-lg text-[10px] font-medium transition-all cursor-pointer",
              digestView
                ? "bg-background text-primary shadow-2xs font-semibold"
                : "text-muted-foreground hover:text-foreground",
            )}
          >
            <History className="size-3.5 mb-0.5" />
            <span>{t("recally.nav.digest")}</span>
          </button>
        </div>

        {/* Inline Stats Summary */}
        {!digestView && digest && (
          <div className="mt-2.5 flex flex-wrap items-center gap-3 border-t border-border/10 pt-2 pb-0.5">
            <StatCard value={digest.total_articles ?? 0} label={t("recally.stat.total")} />
            <StatCard value={digest.unread_count ?? 0} label={t("recally.stat.unread")} />
            <StatCard value={digest.starred_count ?? 0} label={t("recally.stat.starred")} />
            <StatCard
              value={digest.saved_yesterday_count ?? 0}
              label={t("recally.stat.savedYesterday")}
            />
          </div>
        )}
      </div>

      {/* Toolbar: Search and Filter Toggle */}
      <div className="shrink-0 border-b border-border/60 bg-card/45 px-3 py-2 flex items-center gap-2">
        <div className="relative flex-1">
          <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 w-3.5 h-3.5 text-muted-foreground/45 pointer-events-none" />
          <input
            type="text"
            value={searchText}
            onChange={(e) => setSearchText(e.target.value)}
            placeholder={t("recally.searchPlaceholder")}
            className="w-full pl-8 pr-3 py-1.5 text-[11px] font-mono rounded-lg bg-muted/20 border border-border/40 hover:border-border/75 focus:border-primary/40 focus:ring-2 focus:ring-primary/5 focus:outline-none transition-all duration-150 text-foreground placeholder:text-muted-foreground/40 shadow-2xs"
          />
        </div>
        <button
          onClick={() => setShowFiltersPanel(!showFiltersPanel)}
          className={cn(
            "relative p-1.5 rounded-lg border transition-all cursor-pointer flex items-center justify-center",
            showFiltersPanel || activeFiltersCount > 0
              ? "bg-primary/5 text-primary border-primary/25 shadow-2xs"
              : "bg-muted/15 border-border/40 hover:border-border/80 text-muted-foreground hover:text-foreground",
          )}
          title="Filters & RSS Feeds"
        >
          <SlidersHorizontal className="size-4" />
          {activeFiltersCount > 0 && (
            <span className="absolute -top-1 -right-1 flex h-4 w-4 items-center justify-center rounded-full bg-primary text-[8px] font-bold text-primary-foreground">
              {activeFiltersCount}
            </span>
          )}
        </button>
      </div>

      {/* Advanced Filters Panel */}
      {showFiltersPanel && (
        <div className="shrink-0 border-b border-border/60 bg-muted/10 p-3.5 space-y-4 max-h-[360px] overflow-y-auto animate-in slide-in-from-top-2 duration-200">
          {/* Status Filters */}
          <div>
            <div className="text-[10px] font-mono font-semibold uppercase tracking-wider text-muted-foreground/60 mb-1.5 pl-0.5">
              {t("recally.section.status")}
            </div>
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

          {/* Source Filters */}
          <div>
            <div className="text-[10px] font-mono font-semibold uppercase tracking-wider text-muted-foreground/60 mb-1.5 pl-0.5">
              {t("recally.section.sources")}
            </div>
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

          {/* Tags */}
          {sortedTags.length > 0 && (
            <div>
              <div className="text-[10px] font-mono font-semibold uppercase tracking-wider text-muted-foreground/60 mb-1.5 pl-0.5">
                {t("recally.section.tags")}
              </div>
              <div className="flex flex-wrap gap-1.5">
                {visibleTags.map((tag) => (
                  <FilterChip
                    key={tag}
                    label={`${tag} (${tagCounts[tag]})`}
                    active={tagFilter === tag}
                    onClick={() => setTagFilter(tagFilter === tag ? null : tag)}
                  />
                ))}
                {hasMoreTags && (
                  <button
                    onClick={() => setShowAllTags(!showAllTags)}
                    className="rounded-full bg-muted/40 border border-border/20 px-2.5 py-0.5 text-[10px] font-mono text-muted-foreground transition-all hover:text-foreground cursor-pointer"
                  >
                    {showAllTags
                      ? t("recally.tags.less")
                      : t("recally.tags.more", { count: sortedTags.length - 10 })}
                  </button>
                )}
              </div>
            </div>
          )}

          {/* Feeds Section */}
          <div>
            <div className="text-[10px] font-mono font-semibold uppercase tracking-wider text-muted-foreground/60 mb-1.5 pl-0.5">
              {t("recally.section.feeds")}
            </div>
            <div className="space-y-2">
              <div className="flex items-center gap-1.5">
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
                  className="h-7 flex-1 rounded-lg bg-muted/30 border border-border/40 px-2.5 text-[11px] font-mono placeholder:text-muted-foreground/45 hover:border-border focus:border-primary/40 focus:ring-1 focus:ring-primary/10 focus:outline-none transition-all duration-150 text-foreground"
                />
                <button
                  onClick={() => {
                    if (feedUrl.trim()) {
                      createFeedMut.mutate({ body: { url: feedUrl.trim() } });
                    }
                  }}
                  disabled={createFeedMut.isPending || !feedUrl.trim()}
                  className="inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-lg bg-muted/30 border border-border/40 hover:bg-muted transition-colors disabled:opacity-50 cursor-pointer"
                  title={t("recally.feeds.addFeed")}
                >
                  {createFeedMut.isPending ? (
                    <RefreshCw className="size-3.5 animate-spin text-muted-foreground" />
                  ) : (
                    <Plus className="size-3.5 text-muted-foreground" />
                  )}
                </button>
              </div>

              {feeds.length > 0 && (
                <div className="max-h-36 overflow-y-auto space-y-1 pr-1 border border-border/20 rounded-lg p-1.5 bg-card/35">
                  {feeds.map((feed: Feed) => (
                    <div
                      key={feed.id}
                      className="flex items-center justify-between gap-1.5 py-0.5 border-b border-border/10 last:border-0"
                    >
                      <span
                        className="truncate text-[11px] text-foreground/80 font-medium"
                        title={feed.title || feed.url}
                      >
                        {feed.title || feed.url}
                      </span>
                      <button
                        onClick={() => pollFeedMut.mutate({ path: { id: feed.id } })}
                        disabled={pollFeedMut.isPending}
                        className="inline-flex shrink-0 items-center gap-1 rounded-md bg-muted/30 border border-border/30 px-1.5 py-0.5 font-mono text-[9px] text-muted-foreground hover:text-foreground transition-colors disabled:opacity-50 cursor-pointer"
                      >
                        {pollFeedMut.isPending && pollFeedMut.variables?.path.id === feed.id ? (
                          <RefreshCw className="size-2.5 animate-spin" />
                        ) : feedPollResults[feed.id]?.error ? (
                          <>
                            <AlertCircle className="size-2.5 text-destructive" />
                            <span className="text-destructive">Err</span>
                          </>
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
              )}
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
                <p className="text-xs font-mono text-muted-foreground/60">
                  {t("recally.digest.noHistory")}
                </p>
              </div>
            )}
            {storedDigests.map((d) => (
              <button
                key={d.id}
                type="button"
                onClick={() => onSelectDigest(d.date)}
                className={cn(
                  "w-full rounded-xl border p-3.5 text-left transition-all duration-200 cursor-pointer",
                  selectedDigestDate === d.date
                    ? "border-primary/20 bg-primary/[0.03] text-foreground shadow-xs ring-1 ring-primary/10"
                    : "border-border/40 bg-card/45 hover:border-border/80 hover:bg-card/75 hover:scale-[1.01] hover:shadow-2xs",
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
                <p className="text-xs font-mono text-muted-foreground/60">
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
                onClick={() => setSelectedId(article.id)}
                t={t}
              />
            ))}
          </>
        )}
      </div>
    </section>
  );
}
