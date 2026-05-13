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
  AlertCircle,
  X,
  BookOpen,
  Plus,
  Sparkles,
  ChevronDown,
  PanelLeft,
} from "lucide-react";
import { cn } from "@/lib/utils";
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
  const [summaryExpanded, setSummaryExpanded] = useState(true);
  const [feedPollResults, setFeedPollResults] = useState<
    Record<string, { newCount: number; error?: string }>
  >({});
  const [leftOpen, setLeftOpen] = useState(true);
  const [tagFilter, setTagFilter] = useState<string | null>(null);
  const [showAllTags, setShowAllTags] = useState(false);

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
  const displayArticles = tagFilter
    ? articles.filter((a) => a.tags?.includes(tagFilter))
    : articles;

  const allTags = articles.flatMap((a) => a.tags ?? []);
  const tagCounts = allTags.reduce(
    (acc, tag) => {
      acc[tag] = (acc[tag] || 0) + 1;
      return acc;
    },
    {} as Record<string, number>,
  );
  const sortedTags = Object.entries(tagCounts)
    .sort((a, b) => b[1] - a[1])
    .map(([tag]) => tag);
  const visibleTags = showAllTags ? sortedTags : sortedTags.slice(0, 10);
  const hasMoreTags = sortedTags.length > 10;

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
    setTagFilter(null);
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
    <div className="min-h-0 flex-1 overflow-auto bg-background">
      {!selectedId ? (
        <div className="flex h-full items-center justify-center px-8 text-center">
          <div className="max-w-72">
            <div className="mx-auto mb-3 flex size-10 items-center justify-center rounded-md border border-border bg-card text-muted-foreground">
              <BookOpen className="size-5" />
            </div>
            <p className="text-sm font-medium text-foreground">{t("recally.reader.empty")}</p>
          </div>
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
        <div className="mx-auto max-w-[760px] px-6 py-6">
          <div className="mb-5 flex flex-wrap items-center justify-between gap-3 border-b border-border pb-4">
            <div className="flex flex-wrap gap-1.5 text-xs font-mono text-muted-foreground">
              <StatusBadge status={selectedArticle.status} t={t} />
              <span className="rounded-full border border-border bg-card px-2 py-0.5">
                {t(SOURCE_LABEL_KEYS[selectedArticle.source_type])}
              </span>
              <span className="rounded-full border border-border bg-card px-2 py-0.5">
                {formatSavedAt(selectedArticle.saved_at, t)}
              </span>
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
                  className="inline-flex items-center gap-1 rounded-md border border-border bg-card px-2 py-1 text-xs transition-colors hover:bg-accent disabled:opacity-50"
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
                  className="inline-flex items-center gap-1 rounded-md border border-border bg-card px-2 py-1 text-xs transition-colors hover:bg-accent disabled:opacity-50"
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
                  className="inline-flex items-center gap-1 rounded-md border border-border bg-card px-2 py-1 text-xs transition-colors hover:bg-accent disabled:opacity-50"
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
                className="inline-flex items-center gap-1 rounded-md border border-border bg-card px-2 py-1 text-xs transition-colors hover:bg-accent disabled:opacity-50"
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
                    className="rounded-md border border-border bg-card px-2 py-1 text-xs transition-colors hover:bg-accent"
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
            <h2 className="mb-2 text-2xl font-semibold leading-tight tracking-tight text-foreground">
              {selectedArticle.title}
            </h2>
            {selectedArticle.author && (
              <p className="mb-3 font-mono text-xs text-muted-foreground">
                {selectedArticle.author}
              </p>
            )}
            {selectedArticle.summary && (
              <div className="mb-4 rounded-md border border-border bg-accent/5 p-3">
                <button
                  type="button"
                  onClick={() => setSummaryExpanded(!summaryExpanded)}
                  className="flex w-full items-center gap-1.5"
                >
                  <Sparkles className="size-3.5 text-primary" />
                  <span className="text-[11px] font-semibold uppercase tracking-wider text-primary">
                    {t("recally.summary.label")}
                  </span>
                  <ChevronDown
                    className={`ml-auto size-3 text-muted-foreground transition-transform ${summaryExpanded ? "rotate-180" : ""}`}
                  />
                </button>
                {summaryExpanded && (
                  <div className="prose prose-sm mt-1.5 max-w-none text-foreground prose-headings:text-foreground prose-a:text-primary">
                    <Streamdown>{selectedArticle.summary}</Streamdown>
                  </div>
                )}
              </div>
            )}
            {selectedArticle.content ? (
              <div className="prose prose-sm max-w-none text-foreground prose-headings:text-foreground prose-a:text-primary">
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
    <div className="flex h-[calc(100vh-3.5rem)] min-h-0 overflow-hidden bg-background">
      {/* Left sidebar */}
      <aside
        className={cn(
          "hidden md:flex flex-shrink-0 flex-col overflow-auto border-r border-border bg-muted/30 transition-all duration-200 ease-out",
          leftOpen
            ? "w-[260px] min-w-[260px]"
            : "w-0 min-w-0 overflow-hidden opacity-0 pointer-events-none",
        )}
      >
        <div className="space-y-5 p-3">
          <div className="rounded-md border border-border bg-card p-3 shadow-sm">
            <div className="flex items-center gap-2">
              <div className="flex size-8 items-center justify-center rounded-md bg-foreground text-background">
                <BookOpen className="size-4" />
              </div>
              <div className="min-w-0">
                <div className="text-sm font-semibold tracking-tight text-foreground">
                  {t("recally.title")}
                </div>
                <div className="truncate text-[11px] text-muted-foreground">
                  {t("recally.subtitle")}
                </div>
              </div>
            </div>
          </div>

          {/* Views */}
          <div>
            <div className="mb-1.5 px-2 text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
              {t("recally.section.library")}
            </div>
            <nav className="space-y-0.5">
              <SidebarNavItem
                label={t("recally.nav.inbox")}
                count={digest?.total_articles}
                active={statusFilter === null && starredFilter === null && tagFilter === null}
                onClick={clearFilters}
              />
              <SidebarNavItem
                label={t("recally.nav.starred")}
                count={digest?.starred_count}
                active={starredFilter === true && tagFilter === null}
                onClick={() => {
                  setStarredFilter(true);
                  setStatusFilter(null);
                  setSourceTypeFilter(null);
                  setTagFilter(null);
                }}
              />
              <SidebarNavItem
                label={t("recally.nav.archive")}
                count={digest?.archived_count}
                active={statusFilter === "archived" && tagFilter === null}
                onClick={() => {
                  setStatusFilter("archived");
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
                  Tags
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
                      {showAllTags ? "Less" : `+${sortedTags.length - 10}`}
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

      {/* Main area */}
      <div
        className="flex-1 min-w-0 grid grid-cols-1 overflow-hidden xl:grid-cols-[var(--recally-center-width)_1fr]"
        style={{ "--recally-center-width": `${centerWidth}px` } as CSSProperties}
      >
        {/* Center article list */}
        <section className="flex min-h-0 flex-col overflow-hidden border-r border-border bg-background">
          {/* Header with stats */}
          <div className="shrink-0 border-b border-border bg-background px-4 py-4">
            <div className="flex items-start justify-between gap-3">
              <div className="flex items-center gap-2">
                <button
                  onClick={() => setLeftOpen((v) => !v)}
                  className="hidden shrink-0 text-muted-foreground transition-colors hover:text-foreground md:inline-flex"
                  title="Toggle sidebar"
                >
                  <PanelLeft className="size-4" />
                </button>
                <div>
                  <h1 className="text-xl font-semibold tracking-tight text-foreground">
                    {t("recally.title")}
                  </h1>
                  <p className="mt-0.5 text-xs text-muted-foreground">{t("recally.subtitle")}</p>
                </div>
              </div>
              <div className="hidden rounded-md border border-border bg-card px-2 py-1 font-mono text-[11px] text-muted-foreground md:block">
                {displayArticles.length} / 50
              </div>
            </div>
            <div className="mt-3 space-y-2 md:hidden">
              <div className="relative">
                <Search className="pointer-events-none absolute left-3 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
                <input
                  type="text"
                  value={searchText}
                  onChange={(e) => setSearchText(e.target.value)}
                  placeholder={t("recally.searchPlaceholder")}
                  className="h-9 w-full rounded-md border border-border bg-background pl-8 pr-3 text-sm focus:outline-none focus:ring-1 focus:ring-ring"
                />
              </div>
              <div className="flex gap-1.5 overflow-x-auto pb-1">
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
            {digest && (
              <div className="mt-3 grid grid-cols-2 gap-2 lg:grid-cols-4">
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
          <div className="flex-1 space-y-1 overflow-auto p-2">
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
            {!articlesQuery.isLoading && !articlesQuery.isError && displayArticles.length === 0 && (
              <div className="flex flex-col items-center justify-center h-32 text-sm text-muted-foreground">
                <p className="font-medium">{t("recally.empty.noArticles")}</p>
                <p className="text-xs mt-1">{t("recally.empty.noArticlesDesc")}</p>
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
          </div>
        </section>

        {/* Right reader panel */}
        <aside className="relative hidden min-h-0 flex-col bg-background xl:flex">
          <button
            type="button"
            aria-label="Resize list"
            onMouseDown={startResize}
            className="absolute inset-y-0 left-0 z-10 w-2 -translate-x-1 cursor-col-resize border-l border-border transition-colors hover:bg-accent"
          />
          {readerPanel}
        </aside>
      </div>
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
      className={`flex w-full items-center justify-between rounded-md px-2 py-1.5 text-sm transition-colors ${
        active
          ? "bg-accent font-medium text-foreground"
          : "text-muted-foreground hover:bg-accent/50 hover:text-foreground"
      }`}
    >
      <span>{label}</span>
      {count !== undefined && (
        <span className="font-mono text-xs text-muted-foreground">{count}</span>
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
      className={`rounded-full border px-2 py-1 text-xs transition-colors ${
        active
          ? "border-input bg-accent font-medium text-foreground"
          : "border-border bg-background text-muted-foreground hover:bg-accent/50 hover:text-foreground"
      }`}
    >
      {label}
    </button>
  );
}

function StatCard({ value, label }: { value: number; label: string }) {
  return (
    <div className="rounded-md border border-border bg-card px-3 py-2 shadow-sm">
      <div className="font-mono text-lg font-semibold text-foreground">{value}</div>
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
      className={`w-full rounded-md border p-3 text-left transition-colors ${
        selected
          ? "border-input bg-accent/70 shadow-sm"
          : "border-border bg-card hover:bg-accent/30"
      }`}
    >
      <div className="flex items-start justify-between gap-2">
        <h3 className="text-sm font-medium leading-snug text-foreground">{article.title}</h3>
        {article.starred && (
          <Star className="size-4 text-amber-500 fill-amber-500 shrink-0 mt-0.5" />
        )}
      </div>
      {article.summary && (
        <div className="mt-1.5 flex items-start gap-1.5">
          <Sparkles className="mt-0.5 size-3 shrink-0 text-primary" />
          <p className="line-clamp-2 text-xs leading-relaxed text-muted-foreground">
            {article.summary}
          </p>
        </div>
      )}
      <div className="mt-2 flex flex-wrap items-center gap-1.5 font-mono text-xs">
        <StatusBadge status={article.status} t={t} />
        <span className="text-muted-foreground">{t(SOURCE_LABEL_KEYS[article.source_type])}</span>
        <span className="text-muted-foreground">{formatSavedAt(article.saved_at, t)}</span>
        {article.tags?.map((tag) => (
          <span key={tag} className="rounded-full bg-muted px-1.5 py-0.5 text-muted-foreground">
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
      ? "border-info/20 bg-info/8 text-info-foreground"
      : status === "read"
        ? "border-success/20 bg-success/8 text-success-foreground"
        : "border-border bg-muted text-muted-foreground";

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
          : "border-success/20 bg-success/8 text-success-foreground"
      }`}
    >
      {toast.message}
    </div>
  );
}
