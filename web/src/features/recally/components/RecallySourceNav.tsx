import { useState, type Dispatch, type SetStateAction } from "react";
import {
  Inbox,
  Star,
  Archive,
  History,
  Tag,
  Rss,
  Plus,
  RefreshCw,
  ChevronDown,
} from "lucide-react";
import type { Feed } from "@/lib/api-client/types.gen";
import type { TFunction } from "../constants";
import { SidebarNavItem } from "./SidebarNavItem";
import { cn } from "@/lib/utils";

export function RecallySourceNav({
  t,
  digest,
  digestView,
  starredFilter,
  statusFilter,
  tagFilter,
  selectInbox,
  selectStarred,
  selectArchive,
  selectDigest,
  selectTag,
  sortedTags,
  visibleTags,
  showAllTags,
  setShowAllTags,
  tagCounts,
  feeds,
  feedUrl,
  setFeedUrl,
  createFeedMut,
  pollFeedMut,
  feedPollResults,
}: {
  t: TFunction;
  digest:
    | {
        total_articles?: number;
        unread_count?: number;
        starred_count?: number;
        archived_count?: number;
        saved_yesterday_count?: number;
      }
    | undefined;
  digestView: boolean;
  starredFilter: boolean | null;
  statusFilter: string | null;
  tagFilter: string | null;
  selectInbox: () => void;
  selectStarred: () => void;
  selectArchive: () => void;
  selectDigest: () => void;
  selectTag: (tag: string | null) => void;
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
  const [tagsExpanded, setTagsExpanded] = useState(true);
  const [feedsExpanded, setFeedsExpanded] = useState(true);
  const hasMoreTags = sortedTags.length > 10;

  const isInbox =
    !digestView && statusFilter === null && starredFilter === null && tagFilter === null;
  const isStarred = !digestView && starredFilter === true && tagFilter === null;
  const isArchive = !digestView && statusFilter === "archived" && tagFilter === null;

  return (
    <div className="flex min-h-0 w-full flex-col overflow-hidden">
      {/* Library */}
      <div className="shrink-0 px-2 pt-2 pb-1">
        <div className="text-xs font-mono font-semibold text-muted-foreground px-3 pb-1">
          {t("recally.section.library")}
        </div>
        <div className="space-y-0.5">
          <SidebarNavItem
            icon={<Inbox className="size-4" />}
            label={t("recally.nav.inbox")}
            count={digest?.total_articles ?? 0}
            active={isInbox}
            onClick={selectInbox}
          />
          <SidebarNavItem
            icon={<Star className="size-4" />}
            label={t("recally.nav.starred")}
            count={digest?.starred_count ?? 0}
            active={isStarred}
            onClick={selectStarred}
          />
          <SidebarNavItem
            icon={<Archive className="size-4" />}
            label={t("recally.nav.archive")}
            count={digest?.archived_count ?? 0}
            active={isArchive}
            onClick={selectArchive}
          />
          <SidebarNavItem
            icon={<History className="size-4" />}
            label={t("recally.nav.digest")}
            count={digest?.saved_yesterday_count ?? 0}
            active={digestView}
            onClick={selectDigest}
          />
        </div>
      </div>

      <div className="my-1 mx-3 border-t border-border" />

      {/* Scrollable feeds + tags */}
      <div className="flex-1 min-h-0 overflow-y-auto px-2 pb-2">
        {/* Feeds section */}
        <div className="mb-2">
          <button
            type="button"
            onClick={() => setFeedsExpanded(!feedsExpanded)}
            className="flex w-full items-center justify-between px-3 py-1 cursor-pointer"
          >
            <span className="text-xs font-mono font-semibold text-muted-foreground">
              {t("recally.section.feeds")}
            </span>
            <ChevronDown
              className={cn(
                "size-3 text-muted-foreground transition-transform duration-120",
                feedsExpanded && "rotate-180",
              )}
            />
          </button>
          {feedsExpanded && (
            <div className="mt-0.5">
              {/* Add feed input */}
              <div className="flex items-center gap-1.5 px-3 mb-1.5">
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
                  className="h-7 flex-1 min-w-0 rounded-md border border-border/40 px-2 text-xs font-mono placeholder:text-muted-foreground/45 hover:border-border/60 focus:border-primary/40 focus:ring-1 focus:ring-primary/10 focus:outline-none transition-all duration-150 text-foreground"
                />
                <button
                  onClick={() => {
                    if (feedUrl.trim()) {
                      createFeedMut.mutate({ body: { url: feedUrl.trim() } });
                    }
                  }}
                  disabled={createFeedMut.isPending || !feedUrl.trim()}
                  className="inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-md border border-border/40 hover:bg-muted transition-colors disabled:opacity-50 cursor-pointer"
                >
                  {createFeedMut.isPending ? (
                    <RefreshCw className="size-3 animate-spin text-muted-foreground" />
                  ) : (
                    <Plus className="size-3 text-muted-foreground" />
                  )}
                </button>
              </div>
              {/* Feed list */}
              <div className="space-y-0.5">
                {feeds.map((feed) => (
                  <div
                    key={feed.id}
                    className="flex items-center justify-between gap-1.5 mx-1 rounded-lg px-2.5 py-1.5 text-[12px] text-muted-foreground hover:bg-muted hover:text-foreground transition-colors"
                  >
                    <div className="flex items-center gap-2 min-w-0">
                      <Rss className="size-3.5 shrink-0" />
                      <span className="truncate" title={feed.title || feed.url}>
                        {feed.title || feed.url}
                      </span>
                    </div>
                    <button
                      onClick={() => pollFeedMut.mutate({ path: { id: feed.id } })}
                      disabled={pollFeedMut.isPending}
                      className="inline-flex shrink-0 items-center rounded-md px-1 py-0.5 font-mono text-xs text-muted-foreground hover:text-foreground transition-colors disabled:opacity-50 cursor-pointer"
                    >
                      {pollFeedMut.isPending && pollFeedMut.variables?.path.id === feed.id ? (
                        <RefreshCw className="size-2.5 animate-spin" />
                      ) : feedPollResults[feed.id]?.error ? (
                        <span className="text-destructive-foreground font-semibold">
                          {t("recally.article.err")}
                        </span>
                      ) : feedPollResults[feed.id] ? (
                        <span className="text-primary font-semibold">
                          +{feedPollResults[feed.id].newCount}
                        </span>
                      ) : (
                        <RefreshCw className="size-2.5" />
                      )}
                    </button>
                  </div>
                ))}
                {feeds.length === 0 && (
                  <div className="px-3 py-2 text-xs font-mono text-muted-foreground/45">
                    {t("recally.feeds.noFeedsDesc")}
                  </div>
                )}
              </div>
            </div>
          )}
        </div>

        {/* Tags section */}
        <div>
          <button
            type="button"
            onClick={() => setTagsExpanded(!tagsExpanded)}
            className="flex w-full items-center justify-between px-3 py-1 cursor-pointer"
          >
            <span className="text-xs font-mono font-semibold text-muted-foreground">
              {t("recally.section.tags")}
            </span>
            <ChevronDown
              className={cn(
                "size-3 text-muted-foreground transition-transform duration-120",
                tagsExpanded && "rotate-180",
              )}
            />
          </button>
          {tagsExpanded && (
            <div className="space-y-0.5 mt-0.5">
              {visibleTags.length > 0 ? (
                <>
                  {visibleTags.map((tag) => (
                    <SidebarNavItem
                      key={tag}
                      icon={<Tag className="size-3.5" />}
                      label={tag}
                      count={tagCounts[tag]}
                      active={tagFilter === tag}
                      onClick={() => selectTag(tagFilter === tag ? null : tag)}
                    />
                  ))}
                  {hasMoreTags && (
                    <button
                      onClick={() => setShowAllTags(!showAllTags)}
                      className="w-full px-3 py-1 text-xs font-mono text-muted-foreground hover:text-foreground transition-colors cursor-pointer text-left"
                    >
                      {showAllTags
                        ? t("recally.tags.less")
                        : t("recally.tags.more", { count: sortedTags.length - 10 })}
                    </button>
                  )}
                </>
              ) : (
                <div className="px-3 py-2 text-xs font-mono text-muted-foreground/45">
                  {t("recally.tags.empty")}
                </div>
              )}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
