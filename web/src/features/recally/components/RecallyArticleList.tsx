import type { Dispatch, SetStateAction } from "react";
import { Search, PanelLeft } from "lucide-react";
import type { Article, ArticleStatus } from "@/lib/api-client/types.gen";
import type { TFunction } from "../constants";
import { ArticleCard } from "./ArticleCard";
import { FilterChip } from "./FilterChip";
import { StatCard } from "./StatCard";

export function RecallyArticleList({
  t,
  displayArticles,
  articlesQuery,
  selectedId,
  setSelectedId,
  digest,
  searchText,
  setSearchText,
  statusFilter,
  setStatusFilter,
  setLeftOpen,
}: {
  t: TFunction;
  displayArticles: Article[];
  articlesQuery: { isLoading: boolean; isError: boolean };
  selectedId: string | null;
  setSelectedId: (id: string | null) => void;
  digest:
    | {
        total_articles?: number;
        unread_count?: number;
        starred_count?: number;
        saved_yesterday_count?: number;
      }
    | undefined;
  searchText: string;
  setSearchText: Dispatch<SetStateAction<string>>;
  statusFilter: ArticleStatus | null;
  setStatusFilter: Dispatch<SetStateAction<ArticleStatus | null>>;
  setLeftOpen: Dispatch<SetStateAction<boolean>>;
}) {
  return (
    <section className="flex min-h-0 flex-col overflow-hidden border-r border-border bg-background">
      {/* Header with stats */}
      <div className="shrink-0 border-b border-border bg-background px-4 py-4">
        <div className="flex items-start justify-between gap-3">
          <div className="flex items-center gap-2">
            <button
              onClick={() => setLeftOpen((v) => !v)}
              className="hidden shrink-0 text-muted-foreground transition-colors hover:text-foreground md:inline-flex"
              title={t("recally.toggleSidebar")}
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
  );
}
