import { useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { Search, Star, Check, EyeOff, Archive, Trash2 } from "lucide-react";
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
} from "@/lib/api-client/@tanstack/react-query.gen";
import type { Article, ArticleStatus, SourceType } from "@/lib/api-client/types.gen";
import type { MessageKey } from "@/lib/i18n/messages";

const SOURCE_TYPES: SourceType[] = ["web", "rss", "github", "pdf", "youtube", "twitter"];

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

function formatSavedAt(iso: string): string {
  const d = new Date(iso);
  const now = new Date();
  const diffMs = now.getTime() - d.getTime();
  const diffDays = Math.floor(diffMs / (1000 * 60 * 60 * 24));
  if (diffDays === 0) return "today";
  if (diffDays === 1) return "yesterday";
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
  const [confirmingDelete, setConfirmingDelete] = useState(false);

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
      setConfirmingDelete(false);
      showToast(t("recally.article.deleted"));
    },
    onError: () => {
      showToast(t("recally.article.deleteFailed"), "error");
    },
  });

  function clearFilters() {
    setStatusFilter(null);
    setStarredFilter(null);
    setSourceTypeFilter(null);
  }

  return (
    <div className="h-full grid grid-cols-1 md:grid-cols-[220px_1fr] xl:grid-cols-[240px_1fr_420px] overflow-hidden bg-background">
      {/* Left sidebar */}
      <aside className="hidden md:flex flex-col border-r border-border overflow-auto">
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

          {/* Feeds placeholder */}
          <div>
            <div className="text-xs font-semibold text-muted-foreground uppercase tracking-wider mb-1.5 px-2">
              {t("recally.section.feeds")}
            </div>
            <div className="mx-1 px-2.5 py-2 text-xs text-muted-foreground border border-dashed border-border rounded-md">
              {t("recally.feeds.placeholder")}
            </div>
          </div>

          {/* Digest note */}
          {digest && (
            <div className="mx-1 px-2.5 py-2 text-xs text-muted-foreground bg-muted/50 rounded-md border border-border">
              <span className="font-medium text-foreground">{t("recally.nav.digest")}: </span>
              {digest.saved_yesterday_count} {t("recally.stat.savedYesterday").toLowerCase()}
              {digest.worth_revisiting_count > 0 && (
                <>. {digest.worth_revisiting_count} worth revisiting.</>
              )}
            </div>
          )}
        </div>
      </aside>

      {/* Center article list */}
      <section className="flex flex-col min-h-0 overflow-hidden">
        {/* Header with stats */}
        <div className="shrink-0 px-4 py-3 border-b border-border">
          <div>
            <h1 className="text-lg font-semibold tracking-tight">{t("recally.title")}</h1>
            <p className="text-xs text-muted-foreground mt-0.5">{t("recally.subtitle")}</p>
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
      <aside className="hidden xl:flex flex-col border-l border-border overflow-hidden bg-background">
        <div className="flex-1 overflow-auto">
          {!selectedId ? (
            <div className="flex items-center justify-center h-full text-sm text-muted-foreground">
              {t("recally.reader.empty")}
            </div>
          ) : articleQuery.isLoading ? (
            <div className="flex items-center justify-center h-full text-sm text-muted-foreground">
              {t("common.loading")}
            </div>
          ) : articleQuery.isError ? (
            <div className="flex items-center justify-center h-full text-sm text-destructive">
              {t("common.error")}
            </div>
          ) : selectedArticle ? (
            <div className="p-4">
              <div className="flex flex-wrap items-center justify-between gap-2 mb-3">
                <div className="flex flex-wrap gap-1.5 text-xs font-mono text-muted-foreground">
                  <StatusBadge status={selectedArticle.status} t={t} />
                  <span>{t(SOURCE_LABEL_KEYS[selectedArticle.source_type])}</span>
                  <span>{formatSavedAt(selectedArticle.saved_at)}</span>
                </div>
                <div className="flex items-center gap-1">
                  {selectedArticle.status !== "read" && (
                    <button
                      onClick={() =>
                        updateArticleMut.mutate({
                          body: { status: "read" },
                          path: { id: selectedArticle.id },
                        })
                      }
                      disabled={updateArticleMut.isPending}
                      className="inline-flex items-center gap-1 px-2 py-1 text-xs rounded-md border border-border hover:bg-accent transition-colors disabled:opacity-50"
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
                      className="inline-flex items-center gap-1 px-2 py-1 text-xs rounded-md border border-border hover:bg-accent transition-colors disabled:opacity-50"
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
                      className="inline-flex items-center gap-1 px-2 py-1 text-xs rounded-md border border-border hover:bg-accent transition-colors disabled:opacity-50"
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
                    className="inline-flex items-center gap-1 px-2 py-1 text-xs rounded-md border border-border hover:bg-accent transition-colors disabled:opacity-50"
                  >
                    <Star
                      className={`size-3 ${selectedArticle.starred ? "fill-amber-500 text-amber-500" : ""}`}
                    />
                    {selectedArticle.starred
                      ? t("recally.action.unstar")
                      : t("recally.action.star")}
                  </button>
                  {confirmingDelete ? (
                    <div className="flex items-center gap-1">
                      <span className="text-xs text-destructive font-medium">
                        {t("recally.deleteConfirm")}
                      </span>
                      <button
                        onClick={() =>
                          deleteArticleMut.mutate({
                            path: { id: selectedArticle.id },
                          })
                        }
                        disabled={deleteArticleMut.isPending}
                        className="px-2 py-1 text-xs rounded-md border border-destructive/30 bg-destructive/10 text-destructive hover:bg-destructive/20 transition-colors disabled:opacity-50"
                      >
                        {t("common.yes")}
                      </button>
                      <button
                        onClick={() => setConfirmingDelete(false)}
                        className="px-2 py-1 text-xs rounded-md border border-border hover:bg-accent transition-colors"
                      >
                        {t("common.no")}
                      </button>
                    </div>
                  ) : (
                    <button
                      onClick={() => setConfirmingDelete(true)}
                      disabled={deleteArticleMut.isPending}
                      className="inline-flex items-center gap-1 px-2 py-1 text-xs rounded-md border border-destructive/30 text-destructive hover:bg-destructive/10 transition-colors disabled:opacity-50"
                    >
                      <Trash2 className="size-3" />
                      {t("common.delete")}
                    </button>
                  )}
                </div>
              </div>
              <article className="max-w-prose">
                <h2 className="text-xl font-semibold tracking-tight mb-1">
                  {selectedArticle.title}
                </h2>
                {selectedArticle.author && (
                  <p className="text-xs text-muted-foreground font-mono mb-4">
                    {selectedArticle.author}
                  </p>
                )}
                {selectedArticle.content ? (
                  <div className="text-sm leading-relaxed text-foreground whitespace-pre-wrap">
                    {selectedArticle.content}
                  </div>
                ) : (
                  <p className="text-sm text-muted-foreground italic">No content available.</p>
                )}
              </article>
            </div>
          ) : (
            <div className="flex items-center justify-center h-full text-sm text-muted-foreground">
              {t("recally.reader.empty")}
            </div>
          )}
        </div>
      </aside>
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
        <span className="text-muted-foreground">{formatSavedAt(article.saved_at)}</span>
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
