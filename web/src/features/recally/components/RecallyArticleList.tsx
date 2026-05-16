import type { Dispatch, SetStateAction } from "react";
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
      {/* Header */}
      <div className="shrink-0 border-b border-border bg-background px-4 py-3">
        <div className="flex items-center justify-between gap-3">
          <div className="flex items-center gap-2">
            <button
              onClick={() => setLeftOpen((v) => !v)}
              className="hidden shrink-0 text-muted-foreground transition-colors hover:text-foreground md:inline-flex"
              title={t("recally.toggleSidebar")}
            >
              <svg
                className="w-4 h-4"
                viewBox="0 0 24 24"
                fill="none"
                stroke="currentColor"
                strokeWidth="1.8"
              >
                <rect x="3" y="3" width="18" height="18" rx="2" />
                <path d="M9 3v18" />
              </svg>
            </button>
            <h1 className="text-[13px] font-semibold tracking-tight text-foreground">
              {t("recally.title")}
            </h1>
          </div>
          <span className="font-mono text-[10px] text-muted-foreground/50 tabular-nums">
            {displayArticles.length} / 50
          </span>
        </div>

        {/* Mobile search and filters */}
        <div className="mt-2.5 space-y-2 md:hidden">
          <div className="relative">
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
            <input
              type="text"
              value={searchText}
              onChange={(e) => setSearchText(e.target.value)}
              placeholder={t("recally.searchPlaceholder")}
              className="w-full pl-7 pr-3 py-1.5 text-xs font-mono rounded-lg bg-muted/50 border border-transparent hover:border-border focus:border-primary/40 focus:outline-none transition-all duration-150 text-foreground placeholder:text-muted-foreground/50"
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

        {/* Inline stats */}
        {digest && (
          <div className="mt-2.5 flex flex-wrap items-center gap-4">
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
          <div className="flex items-center justify-center h-32">
            <div className="w-3 h-3 border border-muted-foreground/30 border-t-muted-foreground/70 rounded-full animate-spin" />
          </div>
        )}
        {articlesQuery.isError && (
          <div className="flex items-center justify-center h-32 text-xs font-mono text-destructive">
            {t("common.error")}
          </div>
        )}
        {!articlesQuery.isLoading && !articlesQuery.isError && displayArticles.length === 0 && (
          <div className="flex flex-col items-center justify-center h-32">
            <p className="text-xs font-mono text-muted-foreground/60">
              {t("recally.empty.noArticles")}
            </p>
            <p className="text-[10px] font-mono text-muted-foreground/40 mt-1">
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
      </div>
    </section>
  );
}
