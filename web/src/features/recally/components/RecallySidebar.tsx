import type { Dispatch, SetStateAction } from "react";
import { Plus, RefreshCw, AlertCircle } from "lucide-react";
import type { Feed, ArticleStatus, SourceType } from "@/lib/api-client/types.gen";
import type { TFunction } from "../constants";
import { SOURCE_TYPES, SOURCE_LABEL_KEYS } from "../constants";
import { SidebarNavItem } from "./SidebarNavItem";
import { FilterChip } from "./FilterChip";

function SectionLabel({ children }: { children: React.ReactNode }) {
  return (
    <div className="px-3.5 pt-3 pb-1.5 text-[10px] font-mono font-medium uppercase tracking-widest text-muted-foreground/70">
      {children}
    </div>
  );
}

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
    <div className="flex flex-col h-full overflow-y-auto">
      {/* Search */}
      <div className="flex-shrink-0 px-2.5 pt-3 pb-1">
        <div className="relative">
          <input
            type="text"
            value={searchText}
            onChange={(e) => setSearchText(e.target.value)}
            placeholder={t("recally.searchPlaceholder")}
            className="w-full pl-7 pr-3 py-1.5 text-xs font-mono rounded-lg bg-muted/50 border border-transparent hover:border-border focus:border-primary/40 focus:outline-none transition-all duration-150 text-foreground placeholder:text-muted-foreground/50"
          />
          <svg
            className="absolute left-2 top-1/2 -translate-y-1/2 w-3 h-3 text-muted-foreground/50 pointer-events-none"
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            strokeWidth="2.5"
          >
            <circle cx="11" cy="11" r="8" />
            <path d="m21 21-4.35-4.35" />
          </svg>
        </div>
      </div>

      {/* Library */}
      <div>
        <SectionLabel>{t("recally.section.library")}</SectionLabel>
        <nav className="space-y-0.5 px-1">
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

      {/* Filters */}
      <div>
        <SectionLabel>{t("recally.section.status")}</SectionLabel>
        <div className="flex flex-wrap gap-1.5 px-3">
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
        <SectionLabel>{t("recally.section.sources")}</SectionLabel>
        <div className="flex flex-wrap gap-1.5 px-3">
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
          <SectionLabel>{t("recally.section.tags")}</SectionLabel>
          <div className="flex flex-wrap gap-1 px-3">
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
                className="rounded-full bg-muted px-2 py-1 text-xs font-mono text-muted-foreground transition-colors hover:text-foreground"
              >
                {showAllTags
                  ? t("recally.tags.less")
                  : t("recally.tags.more", { count: sortedTags.length - 10 })}
              </button>
            )}
          </div>
        </div>
      )}

      {/* Feeds */}
      <div>
        <SectionLabel>{t("recally.section.feeds")}</SectionLabel>
        <div className="px-3 space-y-1.5">
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
              className="h-7 flex-1 rounded-lg bg-muted/50 border border-transparent px-2 text-xs font-mono placeholder:text-muted-foreground/50 hover:border-border focus:border-primary/40 focus:outline-none transition-all duration-150"
            />
            <button
              onClick={() => {
                if (feedUrl.trim()) {
                  createFeedMut.mutate({ body: { url: feedUrl.trim() } });
                }
              }}
              disabled={createFeedMut.isPending || !feedUrl.trim()}
              className="inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-lg bg-muted/50 transition-colors hover:bg-muted disabled:opacity-50"
              title={t("recally.feeds.addFeed")}
            >
              {createFeedMut.isPending ? (
                <RefreshCw className="size-3 animate-spin text-muted-foreground" />
              ) : (
                <Plus className="size-3 text-muted-foreground" />
              )}
            </button>
          </div>
          {feedsQuery.isLoading && (
            <div className="text-xs font-mono text-muted-foreground/50 py-1">
              {t("common.loading")}
            </div>
          )}
          {feedsQuery.isError && (
            <div className="text-xs font-mono text-destructive py-1">{t("common.error")}</div>
          )}
          {!feedsQuery.isLoading && !feedsQuery.isError && feeds.length === 0 && (
            <div className="text-xs font-mono text-muted-foreground/50 py-1">
              {t("recally.feeds.noFeedsDesc")}
            </div>
          )}
          {feeds.map((feed: Feed) => (
            <div key={feed.id} className="flex items-center justify-between gap-1.5 py-0.5">
              <span
                className="truncate text-[12px] text-foreground/80"
                title={feed.title || feed.url}
              >
                {feed.title || feed.url}
              </span>
              <button
                onClick={() => pollFeedMut.mutate({ path: { id: feed.id } })}
                disabled={pollFeedMut.isPending}
                className="inline-flex shrink-0 items-center gap-1 rounded-md bg-muted/50 px-1.5 py-0.5 font-mono text-[10px] transition-colors hover:bg-muted disabled:opacity-50"
                title={t("recally.feeds.poll")}
              >
                {pollFeedMut.isPending && pollFeedMut.variables?.path.id === feed.id ? (
                  <RefreshCw className="size-3 animate-spin text-muted-foreground" />
                ) : feedPollResults[feed.id]?.error ? (
                  <>
                    <AlertCircle className="size-3 text-destructive" />
                    <span className="text-destructive">{t("recally.feeds.pollError")}</span>
                  </>
                ) : feedPollResults[feed.id] ? (
                  <span className="text-primary">
                    {t("recally.feeds.pollNewEntries", {
                      count: feedPollResults[feed.id].newCount,
                    })}
                  </span>
                ) : (
                  <>
                    <RefreshCw className="size-3 text-muted-foreground" />
                    <span className="text-muted-foreground">{t("recally.feeds.poll")}</span>
                  </>
                )}
              </button>
            </div>
          ))}
        </div>
      </div>

      {/* Digest note */}
      {digest && (
        <div className="mx-3 mt-3 rounded-lg bg-muted/30 px-3 py-2 text-xs text-muted-foreground">
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
