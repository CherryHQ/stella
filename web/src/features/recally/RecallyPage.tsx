import { useState, type CSSProperties } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { Streamdown } from "streamdown";
import {
  Search,
  Star,
  Check,
  EyeOff,
  Archive,
  Trash2,
  RefreshCw,
  Rss,
  AlertCircle,
  X,
} from "lucide-react";
import { useI18n } from "@/lib/i18n";
import {
  listArticlesOptions,
  listArticlesQueryKey,
  getArticleOptions,
  getArticleQueryKey,
  getDigestOptions,
  getDigestQueryKey,
  updateArticleMutation,
  deleteArticleMutation,
  listFeedsOptions,
  listFeedsQueryKey,
  createFeedMutation,
  pollFeedMutation,
} from "@/lib/api-client/@tanstack/react-query.gen";
import type { Article, ArticleStatus, SourceType, Feed } from "@/lib/api-client/types.gen";
import type { MessageKey } from "@/lib/i18n/messages";

const SOURCE_TYPES: SourceType[] = ["web", "rss", "github", "pdf", "youtube", "twitter"];
const CENTER_WIDTH_DEFAULT = 420;
const CENTER_WIDTH_MIN = 280;
const CENTER_WIDTH_MAX = 640;

const SOURCE_LABEL_KEYS: Record<SourceType, MessageKey> = {
  web: "recally.source.web",
  rss: "recally.source.rss",
  github: "recally.source.github",
  pdf: "recally.source.pdf",
  youtube: "recally.source.youtube",
  twitter: "recally.source.twitter",
};

const STATUS_LABEL_KEYS: Record<ArticleStatus, MessageKey> = {
  unread: "recally.status.unread",
  read: "recally.status.read",
  archived: "recally.status.archived",
};

function formatSavedAt(iso: string, t: (key: MessageKey) => string): string {
  const d = new Date(iso);
  const now = new Date();
  const diffMs = now.getTime() - d.getTime();
  const diffDays = Math.floor(diffMs / (1000 * 60 * 60 * 24));
  if (diffDays === 0) return t("recally.time.today");
  if (diffDays === 1) return t("recally.time.yesterday");
  return d.toLocaleDateString(undefined, { month: "short", day: "numeric" });
}

export function RecallyPage() {
  const { t } = useI18n();
  const [searchText, setSearchText] = useState("");
  const [statusFilter, setStatusFilter] = useState<ArticleStatus | null>(null);
  const [sourceTypeFilter, setSourceTypeFilter] = useState<SourceType | null>(null);
  const [starredFilter, setStarredFilter] = useState<boolean | null>(null);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [toast, setToast] = useState<{ message: string; type: "success" | "error" } | null>(null);
  const [confirmingDeleteId, setConfirmingDeleteId] = useState<string | null>(null);
  const [feedUrl, setFeedUrl] = useState("");
  const [centerWidth, setCenterWidth] = useState(CENTER_WIDTH_DEFAULT);
  const [feedPollResults, setFeedPollResults] = useState<
    Record<string, { newCount: number; error?: string }>
  >({});

  const showToast = (message: string, type: "success" | "error" = "success") => {
    setToast({ message, type });
    setTimeout(() => setToast(null), 3000);
  };

  const queryClient = useQueryClient();

  const digestQuery = useQuery(getDigestOptions());
  const articlesQuery = useQuery(
    listArticlesOptions({
      query: {
        ...(searchText ? { q: searchText } : {}),
        ...(statusFilter ? { status: statusFilter } : {}),
        ...(sourceTypeFilter ? { source_type: sourceTypeFilter } : {}),
        ...(starredFilter !== null ? { starred: starredFilter } : {}),
        limit: 50,
      },
    }),
  );
  const articleQuery = useQuery({
    ...getArticleOptions({
      path: { id: selectedId ?? "" },
      query: { include: "content" },
    }),
    enabled: !!selectedId,
  });

  const digest = digestQuery.data;
  const articles = articlesQuery.data?.items ?? [];
  const selectedArticle = articleQuery.data;

  const updateArticleMut = useMutation({
    ...updateArticleMutation(),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: listArticlesQueryKey() });
      if (selectedId) {
        void queryClient.invalidateQueries({
          queryKey: getArticleQueryKey({
            path: { id: selectedId },
            query: { include: "content" },
          }),
        });
      }
      void queryClient.invalidateQueries({ queryKey: getDigestQueryKey() });
      showToast(t("recally.article.updated"));
    },
    onError: () => {
      showToast(t("recally.article.updateFailed"), "error");
    },
  });

  const deleteArticleMut = useMutation({
    ...deleteArticleMutation(),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: listArticlesQueryKey() });
      void queryClient.invalidateQueries({ queryKey: getDigestQueryKey() });
      setSelectedId(null);
      setConfirmingDeleteId(null);
      showToast(t("recally.article.deleted"));
    },
    onError: () => {
      showToast(t("recally.article.deleteFailed"), "error");
    },
  });

  const feedsQuery = useQuery(listFeedsOptions());
  const feeds = feedsQuery.data?.items ?? [];

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

  function clearFilters() {
    setStatusFilter(null);
    setStarredFilter(null);
    setSourceTypeFilter(null);
  }

  function startResize(e: React.MouseEvent<HTMLButtonElement>) {
    e.preventDefault();
    const startX = e.clientX;
    const startWidth = centerWidth;

    function onMove(ev: MouseEvent) {
      const nextWidth = startWidth + ev.clientX - startX;
      setCenterWidth(Math.max(CENTER_WIDTH_MIN, Math.min(CENTER_WIDTH_MAX, nextWidth)));
    }

    function onUp() {
      document.removeEventListener("mousemove", onMove);
      document.removeEventListener("mouseup", onUp);
      document.body.style.cursor = "";
      document.body.style.userSelect = "";
    }

    document.body.style.cursor = "col-resize";
    document.body.style.userSelect = "none";
    document.addEventListener("mousemove", onMove);
    document.addEventListener("mouseup", onUp);
  }

  const readerPanel = (
    <div className="min-h-0 flex-1 overflow-auto">
      {!selectedId ? (
        <div className="flex h-full items-center justify-center text-sm text-muted-foreground">
          {t("recally.reader.empty")}
        </div>
      ) : articleQuery.isLoading ? (
        <div className="flex h-full items-center justify-center text-sm text-muted-foreground">
          {t("common.loading")}
        </div>
      ) : articleQuery.isError ? (
        <div className="flex h-full items-center justify-center text-sm text-destructive">
          {t("common.error")}
        </div>
      ) : selectedArticle ? (
        <div className="p-4">
          <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
            <div className="flex flex-wrap gap-1.5 text-xs font-mono text-muted-foreground">
              <StatusBadge status={selectedArticle.status} t={t} />
              <span>{t(SOURCE_LABEL_KEYS[selectedArticle.source_type])}</span>
              <span>{formatSavedAt(selectedArticle.saved_at, t)}</span>
            </div>
            <div className="flex flex-wrap items-center gap-1">
              {selectedArticle.status !== "read" && (
                <button
                  onClick={() =>
                    updateArticleMut.mutate({
                      body: { status: "read" },
                      path: { id: selectedArticle.id },
                    })
                  }
                  disabled={updateArticleMut.isPending}
                  className="inline-flex items-center gap-1 rounded-md border border-border px-2 py-1 text-xs transition-colors hover:bg-accent disabled:opacity-50"
                >
                  <Check className="size-3" />
                  {t("recally.action.markRead")}
                </button>
              )}
              {selectedArticle.status !== "unread" && (
                <button
                  onClick={() =>
                    updateArticleMut.mutate({
                      body: { status: "unread" },
                      path: { id: selectedArticle.id },
                    })
                  }
                  disabled={updateArticleMut.isPending}
                  className="inline-flex items-center gap-1 rounded-md border border-border px-2 py-1 text-xs transition-colors hover:bg-accent disabled:opacity-50"
                >
                  <EyeOff className="size-3" />
                  {t("recally.action.markUnread")}
                </button>
              )}
              {selectedArticle.status !== "archived" && (
                <button
                  onClick={() =>
                    updateArticleMut.mutate({
                      body: { status: "archived" },
                      path: { id: selectedArticle.id },
                    })
                  }
                  disabled={updateArticleMut.isPending}
                  className="inline-flex items-center gap-1 rounded-md border border-border px-2 py-1 text-xs transition-colors hover:bg-accent disabled:opacity-50"
                >
                  <Archive className="size-3" />
                  {t("recally.action.archive")}
                </button>
              )}
              <button
                onClick={() =>
                  updateArticleMut.mutate({
                    body: { starred: !selectedArticle.starred },
                    path: { id: selectedArticle.id },
                  })
                }
                disabled={updateArticleMut.isPending}
                className="inline-flex items-center gap-1 rounded-md border border-border px-2 py-1 text-xs transition-colors hover:bg-accent disabled:opacity-50"
              >
                <Star
                  className={`size-3 ${selectedArticle.starred ? "fill-amber-500 text-amber-500" : ""}`}
                />
                {selectedArticle.starred ? t("recally.action.unstar") : t("recally.action.star")}
              </button>
              {confirmingDeleteId === selectedArticle.id ? (
                <div className="flex items-center gap-1">
                  <span className="text-xs font-medium text-destructive">
                    {t("recally.deleteConfirm")}
                  </span>
                  <button
                    onClick={() =>
                      deleteArticleMut.mutate({
                        path: { id: selectedArticle.id },
                      })
                    }
                    disabled={deleteArticleMut.isPending}
                    className="rounded-md border border-destructive/30 bg-destructive/10 px-2 py-1 text-xs text-destructive transition-colors hover:bg-destructive/20 disabled:opacity-50"
                  >
                    {t("common.yes")}
                  </button>
                  <button
                    onClick={() => setConfirmingDeleteId(null)}
                    className="rounded-md border border-border px-2 py-1 text-xs transition-colors hover:bg-accent"
                  >
                    {t("common.no")}
                  </button>
                </div>
              ) : (
                <button
                  onClick={() => setConfirmingDeleteId(selectedArticle.id)}
                  disabled={deleteArticleMut.isPending}
                  className="inline-flex items-center gap-1 rounded-md border border-destructive/30 px-2 py-1 text-xs text-destructive transition-colors hover:bg-destructive/10 disabled:opacity-50"
                >
                  <Trash2 className="size-3" />
                  {t("common.delete")}
                </button>
              )}
            </div>
          </div>
          <article className="w-full">
            <h2 className="mb-1 text-xl font-semibold tracking-tight">{selectedArticle.title}</h2>
            {selectedArticle.author && (
              <p className="mb-4 font-mono text-xs text-muted-foreground">
                {selectedArticle.author}
              </p>
            )}
            {selectedArticle.content ? (
              <div className="prose prose-sm max-w-none text-foreground">
                <Streamdown>{selectedArticle.content}</Streamdown>
              </div>
            ) : (
              <p className="text-sm italic text-muted-foreground">
                {t("recally.reader.noContent")}
              </p>
            )}
          </article>
        </div>
      ) : (
        <div className="flex h-full items-center justify-center text-sm text-muted-foreground">
          {t("recally.reader.empty")}
        </div>
      )}
    </div>
  );

  return (
    <div
      className="h-[calc(100vh-3.5rem)] min-h-0 grid grid-cols-1 md:grid-cols-[220px_minmax(0,1fr)] xl:grid-cols-[240px_var(--recally-center-width)_1fr] overflow-hidden bg-background"
      style={{ "--recally-center-width": `${centerWidth}px` } as CSSProperties}
    >
      {/* Left sidebar */}
      <aside className="hidden md:flex min-h-0 flex-col border-r border-border overflow-auto">
        <div className="p-3 space-y-4">
          {/* Library */}
          <div>
            <div className="text-xs font-semibold text-muted-foreground uppercase tracking-wider mb-1.5 px-2">
              {t("recally.section.library")}
            </div>
            <nav className="space-y-0.5">
              <SidebarNavItem
                label={t("recally.nav.inbox")}
                count={digest?.total_articles}
                active={statusFilter === null && starredFilter === null}
                onClick={clearFilters}
              />
              <SidebarNavItem
                label={t("recally.nav.starred")}
                count={digest?.starred_count}
                active={starredFilter === true}
                onClick={() => {
                  setStarredFilter(true);
                  setStatusFilter(null);
                  setSourceTypeFilter(null);
                }}
              />
              <SidebarNavItem
                label={t("recally.nav.archive")}
                count={digest?.archived_count}
                active={statusFilter === "archived"}
                onClick={() => {
                  setStatusFilter("archived");
                  setStarredFilter(null);
                  setSourceTypeFilter(null);
                }}
              />
            </nav>
          </div>

          {/* Find */}
          <div>
            <div className="text-xs font-semibold text-muted-foreground uppercase tracking-wider mb-1.5 px-2">
              {t("recally.section.find")}
            </div>
            <div className="relative px-1">
              <Search className="absolute left-3 top-1/2 -translate-y-1/2 size-3.5 text-muted-foreground pointer-events-none" />
              <input
                type="text"
                value={searchText}
                onChange={(e) => setSearchText(e.target.value)}
                placeholder={t("recally.searchPlaceholder")}
                className="w-full h-8 pl-8 pr-3 text-sm bg-muted rounded-md border border-border focus:outline-none focus:ring-1 focus:ring-ring"
              />
            </div>
          </div>

          {/* Status */}
          <div>
            <div className="text-xs font-semibold text-muted-foreground uppercase tracking-wider mb-1.5 px-2">
              {t("recally.section.status")}
            </div>
            <div className="flex flex-wrap gap-1.5 px-1">
              <FilterChip
                label={t("recally.status.all")}
                active={statusFilter === null && starredFilter !== true}
                onClick={() => {
                  setStatusFilter(null);
                  setStarredFilter(null);
                }}
              />
              <FilterChip
                label={t("recally.status.unread")}
                active={statusFilter === "unread"}
                onClick={() => {
                  setStatusFilter("unread");
                  setStarredFilter(null);
                }}
              />
              <FilterChip
                label={t("recally.status.read")}
                active={statusFilter === "read"}
                onClick={() => {
                  setStatusFilter("read");
                  setStarredFilter(null);
                }}
              />
              <FilterChip
                label={t("recally.status.archived")}
                active={statusFilter === "archived"}
                onClick={() => {
                  setStatusFilter("archived");
                  setStarredFilter(null);
                }}
              />
              <FilterChip
                label={t("recally.nav.starred")}
                active={starredFilter === true}
                onClick={() => setStarredFilter(starredFilter === true ? null : true)}
              />
            </div>
          </div>

          {/* Sources */}
          <div>
            <div className="text-xs font-semibold text-muted-foreground uppercase tracking-wider mb-1.5 px-2">
              {t("recally.section.sources")}
            </div>
            <div className="flex flex-wrap gap-1.5 px-1">
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

          {/* Feeds */}
          <div>
            <div className="text-xs font-semibold text-muted-foreground uppercase tracking-wider mb-1.5 px-2">
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
                  className="flex-1 h-7 px-2 text-xs bg-muted rounded-md border border-border focus:outline-none focus:ring-1 focus:ring-ring"
                />
                <button
                  onClick={() => {
                    if (feedUrl.trim()) {
                      createFeedMut.mutate({ body: { url: feedUrl.trim() } });
                    }
                  }}
                  disabled={createFeedMut.isPending || !feedUrl.trim()}
                  className="inline-flex items-center justify-center h-7 w-7 rounded-md border border-border hover:bg-accent transition-colors disabled:opacity-50 shrink-0"
                  title={t("recally.feeds.addFeed")}
                >
                  {createFeedMut.isPending ? (
                    <RefreshCw className="size-3 animate-spin" />
                  ) : (
                    <Rss className="size-3" />
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
                    className="text-xs truncate text-muted-foreground"
                    title={feed.title || feed.url}
                  >
                    {feed.title || feed.url}
                  </span>
                  <button
                    onClick={() => pollFeedMut.mutate({ path: { id: feed.id } })}
                    disabled={pollFeedMut.isPending}
                    className="inline-flex items-center gap-1 px-1.5 py-0.5 rounded text-[10px] font-mono border border-border hover:bg-accent transition-colors disabled:opacity-50 shrink-0"
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
                      <span className="text-green-700">
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
            <div className="mx-1 px-2.5 py-2 text-xs text-muted-foreground bg-muted/50 rounded-md border border-border">
              <span className="font-medium text-foreground">{t("recally.nav.digest")}: </span>
              {t("recally.digest.savedYesterday", { count: digest.saved_yesterday_count })}
              {digest.worth_revisiting_count > 0 && (
                <>
                  . {t("recally.digest.worthRevisiting", { count: digest.worth_revisiting_count })}
                </>
              )}
            </div>
          )}
        </div>
      </aside>

      {/* Center article list */}
      <section className="flex min-h-0 flex-col overflow-hidden">
        {/* Header with stats */}
        <div className="shrink-0 px-4 py-3 border-b border-border">
          <div>
            <h1 className="text-lg font-semibold tracking-tight">{t("recally.title")}</h1>
            <p className="text-xs text-muted-foreground mt-0.5">{t("recally.subtitle")}</p>
          </div>
          <div className="mt-3 space-y-2 md:hidden">
            <div className="relative">
              <Search className="pointer-events-none absolute left-3 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
              <input
                type="text"
                value={searchText}
                onChange={(e) => setSearchText(e.target.value)}
                placeholder={t("recally.searchPlaceholder")}
                className="h-9 w-full rounded-md border border-border bg-muted pl-8 pr-3 text-sm focus:outline-none focus:ring-1 focus:ring-ring"
              />
            </div>
            <div className="flex gap-1.5 overflow-x-auto pb-1">
              <FilterChip
                label={t("recally.status.all")}
                active={statusFilter === null && starredFilter !== true}
                onClick={() => {
                  setStatusFilter(null);
                  setStarredFilter(null);
                }}
              />
              <FilterChip
                label={t("recally.status.unread")}
                active={statusFilter === "unread"}
                onClick={() => {
                  setStatusFilter("unread");
                  setStarredFilter(null);
                }}
              />
              <FilterChip
                label={t("recally.status.read")}
                active={statusFilter === "read"}
                onClick={() => {
                  setStatusFilter("read");
                  setStarredFilter(null);
                }}
              />
              <FilterChip
                label={t("recally.nav.starred")}
                active={starredFilter === true}
                onClick={() => setStarredFilter(starredFilter === true ? null : true)}
              />
            </div>
          </div>
          {digest && (
            <div className="grid grid-cols-2 lg:grid-cols-4 gap-2 mt-3">
              <StatCard value={digest.total_articles} label={t("recally.stat.total")} />
              <StatCard value={digest.unread_count} label={t("recally.stat.unread")} />
              <StatCard value={digest.starred_count} label={t("recally.stat.starred")} />
              <StatCard
                value={digest.saved_yesterday_count}
                label={t("recally.stat.savedYesterday")}
              />
            </div>
          )}
        </div>

        {/* Articles */}
        <div className="flex-1 overflow-auto p-2 space-y-1.5">
          {articlesQuery.isLoading && (
            <div className="flex items-center justify-center h-32 text-sm text-muted-foreground">
              {t("common.loading")}
            </div>
          )}
          {articlesQuery.isError && (
            <div className="flex items-center justify-center h-32 text-sm text-destructive">
              {t("common.error")}
            </div>
          )}
          {!articlesQuery.isLoading && !articlesQuery.isError && articles.length === 0 && (
            <div className="flex flex-col items-center justify-center h-32 text-sm text-muted-foreground">
              <p className="font-medium">{t("recally.empty.noArticles")}</p>
              <p className="text-xs mt-1">{t("recally.empty.noArticlesDesc")}</p>
            </div>
          )}
          {articles.map((article) => (
            <ArticleCard
              key={article.id}
              article={article}
              selected={selectedId === article.id}
              onClick={() => setSelectedId(article.id)}
              t={t}
            />
          ))}
        </div>
      </section>

      {/* Right reader panel */}
      <aside className="relative hidden min-h-0 flex-col border-l border-border bg-background xl:flex">
        <button
          type="button"
          aria-label="Resize list"
          onMouseDown={startResize}
          className="absolute inset-y-0 left-0 z-10 w-2 -translate-x-1 cursor-col-resize transition-colors hover:bg-border/70"
        />
        {readerPanel}
      </aside>
      {selectedId && (
        <div className="fixed inset-x-0 bottom-0 top-14 z-40 flex flex-col border-t border-border bg-background shadow-lg xl:hidden">
          <div className="flex h-11 shrink-0 items-center justify-between border-b border-border px-3">
            <span className="text-sm font-medium">{t("recally.title")}</span>
            <button
              type="button"
              onClick={() => setSelectedId(null)}
              className="rounded-md p-1.5 text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
            >
              <X className="size-4" />
            </button>
          </div>
          {readerPanel}
        </div>
      )}
      <ToastAlert toast={toast} />
    </div>
  );
}

function SidebarNavItem({
  label,
  count,
  active,
  onClick,
}: {
  label: string;
  count?: number;
  active: boolean;
  onClick: () => void;
}) {
  return (
    <button
      onClick={onClick}
      className={`w-full flex items-center justify-between px-2 py-1.5 text-sm rounded-md transition-colors ${
        active
          ? "bg-accent text-accent-foreground font-medium"
          : "text-muted-foreground hover:text-foreground hover:bg-accent/50"
      }`}
    >
      <span>{label}</span>
      {count !== undefined && (
        <span className="text-xs font-mono text-muted-foreground">{count}</span>
      )}
    </button>
  );
}

function FilterChip({
  label,
  active,
  onClick,
}: {
  label: string;
  active: boolean;
  onClick: () => void;
}) {
  return (
    <button
      onClick={onClick}
      className={`px-2 py-1 text-xs rounded-full border transition-colors ${
        active
          ? "border-amber-300 bg-amber-50 text-amber-800 font-medium"
          : "border-border bg-background text-muted-foreground hover:text-foreground"
      }`}
    >
      {label}
    </button>
  );
}

function StatCard({ value, label }: { value: number; label: string }) {
  return (
    <div className="border border-border rounded-lg px-3 py-2 bg-background">
      <div className="text-lg font-semibold font-mono">{value}</div>
      <div className="text-xs text-muted-foreground">{label}</div>
    </div>
  );
}

function ArticleCard({
  article,
  selected,
  onClick,
  t,
}: {
  article: Article;
  selected: boolean;
  onClick: () => void;
  t: (key: MessageKey) => string;
}) {
  return (
    <button
      onClick={onClick}
      className={`w-full text-left p-3 rounded-lg border transition-colors ${
        selected ? "border-border bg-accent/40" : "border-border bg-background hover:bg-accent/20"
      }`}
    >
      <div className="flex items-start justify-between gap-2">
        <h3 className="text-sm font-medium leading-snug">{article.title}</h3>
        {article.starred && (
          <Star className="size-4 text-amber-500 fill-amber-500 shrink-0 mt-0.5" />
        )}
      </div>
      {article.summary && (
        <p className="text-xs text-muted-foreground mt-1 line-clamp-2">{article.summary}</p>
      )}
      <div className="flex flex-wrap items-center gap-1.5 mt-2 text-xs font-mono">
        <StatusBadge status={article.status} t={t} />
        <span className="text-muted-foreground">{t(SOURCE_LABEL_KEYS[article.source_type])}</span>
        <span className="text-muted-foreground">{formatSavedAt(article.saved_at, t)}</span>
        {article.tags?.map((tag) => (
          <span key={tag} className="px-1.5 py-0.5 rounded-full bg-muted text-muted-foreground">
            {tag}
          </span>
        ))}
      </div>
    </button>
  );
}

function StatusBadge({ status, t }: { status: ArticleStatus; t: (key: MessageKey) => string }) {
  const classes =
    status === "unread"
      ? "border-blue-200 bg-blue-50 text-blue-700"
      : status === "read"
        ? "border-green-200 bg-green-50 text-green-700"
        : "border-gray-200 bg-gray-50 text-gray-600";

  return (
    <span className={`px-1.5 py-0.5 rounded-full border ${classes}`}>
      {t(STATUS_LABEL_KEYS[status])}
    </span>
  );
}

function ToastAlert({ toast }: { toast: { message: string; type: "success" | "error" } | null }) {
  if (!toast) return null;
  return (
    <div
      className={`fixed bottom-4 right-4 z-50 w-auto max-w-sm rounded-xl border px-4 py-3 shadow-lg text-sm font-medium ${
        toast.type === "error"
          ? "border-destructive/30 bg-destructive/10 text-destructive-foreground"
          : "border-green-200 bg-green-50 text-green-800"
      }`}
    >
      {toast.message}
    </div>
  );
}
