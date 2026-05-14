import type { Dispatch, SetStateAction } from "react";
import { Search, Plus, RefreshCw, AlertCircle } from "lucide-react";
import type { Feed, ArticleStatus, SourceType } from "@/lib/api-client/types.gen";
import type { TFunction } from "../constants";
import { SOURCE_TYPES, SOURCE_LABEL_KEYS } from "../constants";
import { SidebarNavItem } from "./SidebarNavItem";
import { FilterChip } from "./FilterChip";

export function RecallySidebar({
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
  digest,
  digestView,
  setDigestView,
  sortedTags,
  visibleTags,
  hasMoreTags,
  tagCounts,
  feeds,
  feedsQuery,
  feedUrl,
  setFeedUrl,
  createFeedMut,
  pollFeedMut,
  feedPollResults,
  clearFilters,
}: {
  t: TFunction;
  searchText: string;
  setSearchText: Dispatch<SetStateAction<string>>;
  statusFilter: ArticleStatus | null;
  setStatusFilter: Dispatch<SetStateAction<ArticleStatus | null>>;
  sourceTypeFilter: SourceType | null;
  setSourceTypeFilter: Dispatch<SetStateAction<SourceType | null>>;
  starredFilter: boolean | null;
  setStarredFilter: Dispatch<SetStateAction<boolean | null>>;
  tagFilter: string | null;
  setTagFilter: Dispatch<SetStateAction<string | null>>;
  showAllTags: boolean;
  setShowAllTags: Dispatch<SetStateAction<boolean>>;
  digest:
    | {
        total_articles?: number;
        starred_count?: number;
        archived_count?: number;
        saved_yesterday_count?: number;
        worth_revisiting_count?: number;
      }
    | undefined;
  digestView: boolean;
  setDigestView: Dispatch<SetStateAction<boolean>>;
  sortedTags: string[];
  visibleTags: string[];
  hasMoreTags: boolean;
  tagCounts: Record<string, number>;
  feeds: Feed[];
  feedsQuery: { isLoading: boolean; isError: boolean };
  feedUrl: string;
  setFeedUrl: Dispatch<SetStateAction<string>>;
  createFeedMut: { isPending: boolean; mutate: (args: { body: { url: string } }) => void };
  pollFeedMut: {
    isPending: boolean;
    mutate: (args: { path: { id: string } }) => void;
    variables?: { path: { id: string } };
  };
  feedPollResults: Record<string, { newCount: number; error?: string }>;
  clearFilters: () => void;
}) {
  return (
    <div className="space-y-5 p-3">
      {/* Views */}
      <div>
        <div className="mb-1.5 px-2 text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
          {t("recally.section.library")}
        </div>
        <nav className="space-y-0.5">
          <SidebarNavItem
            label={t("recally.nav.inbox")}
            count={digest?.total_articles}
            active={
              !digestView && statusFilter === null && starredFilter === null && tagFilter === null
            }
            onClick={clearFilters}
          />
          <SidebarNavItem
            label={t("recally.nav.starred")}
            count={digest?.starred_count}
            active={!digestView && starredFilter === true && tagFilter === null}
            onClick={() => {
              setDigestView(false);
              setStarredFilter(true);
              setStatusFilter(null);
              setSourceTypeFilter(null);
              setTagFilter(null);
            }}
          />
          <SidebarNavItem
            label={t("recally.nav.archive")}
            count={digest?.archived_count}
            active={!digestView && statusFilter === "archived" && tagFilter === null}
            onClick={() => {
              setDigestView(false);
              setStatusFilter("archived");
              setStarredFilter(null);
              setSourceTypeFilter(null);
              setTagFilter(null);
            }}
          />
          <SidebarNavItem
            label={t("recally.nav.digest")}
            count={digest?.saved_yesterday_count}
            active={digestView}
            onClick={() => {
              setDigestView(true);
              setStatusFilter(null);
              setStarredFilter(null);
              setSourceTypeFilter(null);
              setTagFilter(null);
            }}
          />
        </nav>
      </div>

      {/* Refine */}
      <div className="space-y-3">
        <div className="mb-1.5 px-2 text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
          {t("recally.section.find")}
        </div>
        <div className="relative px-1">
          <Search className="pointer-events-none absolute left-3 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
          <input
            type="text"
            value={searchText}
            onChange={(e) => setSearchText(e.target.value)}
            placeholder={t("recally.searchPlaceholder")}
            className="h-8 w-full rounded-md border border-border bg-background pl-8 pr-3 text-sm placeholder:text-muted-foreground focus:outline-none focus:ring-1 focus:ring-ring"
          />
        </div>

        <div>
          <div className="mb-1 px-2 text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
            {t("recally.section.status")}
          </div>
          <div className="flex flex-wrap gap-1.5 px-1">
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

        <div>
          <div className="mb-1 px-2 text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
            {t("recally.section.sources")}
          </div>
          <div className="flex flex-wrap gap-1.5 px-1">
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

        {sortedTags.length > 0 && (
          <div>
            <div className="mb-1 px-2 text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
              {t("recally.section.tags")}
            </div>
            <div className="flex flex-wrap gap-1 px-1">
              {visibleTags.map((tag) => (
                <FilterChip
                  key={tag}
                  label={`${tag} ${tagCounts[tag]}`}
                  active={tagFilter === tag}
                  onClick={() => setTagFilter(tagFilter === tag ? null : tag)}
                />
              ))}
              {hasMoreTags && (
                <button
                  onClick={() => setShowAllTags(!showAllTags)}
                  className="rounded-full border border-border bg-background px-2 py-1 text-xs text-muted-foreground transition-colors hover:bg-accent/50 hover:text-foreground"
                >
                  {showAllTags
                    ? t("recally.tags.less")
                    : t("recally.tags.more", { count: sortedTags.length - 10 })}
                </button>
              )}
            </div>
          </div>
        )}
      </div>

      {/* Feeds */}
      <div>
        <div className="mb-1.5 px-2 text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
          {t("recally.section.feeds")}
        </div>
        <div className="px-1 space-y-1.5">
          <div className="flex items-center gap-1">
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
              className="h-7 flex-1 rounded-md border border-border bg-background px-2 text-xs placeholder:text-muted-foreground focus:outline-none focus:ring-1 focus:ring-ring"
            />
            <button
              onClick={() => {
                if (feedUrl.trim()) {
                  createFeedMut.mutate({ body: { url: feedUrl.trim() } });
                }
              }}
              disabled={createFeedMut.isPending || !feedUrl.trim()}
              className="inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-md border border-border bg-background transition-colors hover:bg-accent disabled:opacity-50"
              title={t("recally.feeds.addFeed")}
            >
              {createFeedMut.isPending ? (
                <RefreshCw className="size-3 animate-spin" />
              ) : (
                <Plus className="size-3" />
              )}
            </button>
          </div>
          {feedsQuery.isLoading && (
            <div className="text-xs text-muted-foreground py-1">{t("common.loading")}</div>
          )}
          {feedsQuery.isError && (
            <div className="text-xs text-destructive py-1">{t("common.error")}</div>
          )}
          {!feedsQuery.isLoading && !feedsQuery.isError && feeds.length === 0 && (
            <div className="text-xs text-muted-foreground py-1">
              {t("recally.feeds.noFeedsDesc")}
            </div>
          )}
          {feeds.map((feed: Feed) => (
            <div key={feed.id} className="flex items-center justify-between gap-1.5 py-0.5">
              <span
                className="truncate text-xs text-muted-foreground"
                title={feed.title || feed.url}
              >
                {feed.title || feed.url}
              </span>
              <button
                onClick={() => pollFeedMut.mutate({ path: { id: feed.id } })}
                disabled={pollFeedMut.isPending}
                className="inline-flex shrink-0 items-center gap-1 rounded border border-border bg-background px-1.5 py-0.5 font-mono text-[10px] transition-colors hover:bg-accent disabled:opacity-50"
                title={t("recally.feeds.poll")}
              >
                {pollFeedMut.isPending && pollFeedMut.variables?.path.id === feed.id ? (
                  <RefreshCw className="size-3 animate-spin" />
                ) : feedPollResults[feed.id]?.error ? (
                  <>
                    <AlertCircle className="size-3 text-destructive" />
                    <span className="text-destructive">{t("recally.feeds.pollError")}</span>
                  </>
                ) : feedPollResults[feed.id] ? (
                  <span className="text-success-foreground">
                    {t("recally.feeds.pollNewEntries", {
                      count: feedPollResults[feed.id].newCount,
                    })}
                  </span>
                ) : (
                  <>
                    <RefreshCw className="size-3" />
                    <span>{t("recally.feeds.poll")}</span>
                  </>
                )}
              </button>
            </div>
          ))}
        </div>
      </div>

      {/* Digest note */}
      {digest && (
        <div className="mx-1 rounded-md border border-border bg-card px-2.5 py-2 text-xs text-muted-foreground shadow-sm">
          <span className="font-medium text-foreground">{t("recally.nav.digest")}: </span>
          {t("recally.digest.savedYesterday", { count: digest.saved_yesterday_count ?? 0 })}
          {(digest.worth_revisiting_count ?? 0) > 0 && (
            <>
              . {t("recally.digest.worthRevisiting", { count: digest.worth_revisiting_count ?? 0 })}
            </>
          )}
        </div>
      )}
    </div>
  );
}
