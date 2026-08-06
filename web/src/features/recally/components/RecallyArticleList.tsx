import { useState, type Dispatch, type SetStateAction } from "react";
import { SlidersHorizontal, Search } from "lucide-react";
import { useSidebar } from "@/components/ui/sidebar";
import type {
  Article,
  ArticleStatus,
  SourceType,
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
  searchText,
  setSearchText,
  statusFilter,
  setStatusFilter,
  sourceTypeFilter,
  setSourceTypeFilter,
  digestView,
  storedDigests,
  storedDigestsLoading,
  selectedDigestDate,
  onSelectDigest,
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
  sourceTypeFilter: SourceType | null;
  setSourceTypeFilter: Dispatch<SetStateAction<SourceType | null>>;
  digestView: boolean;
  storedDigests: StoredDigestSummary[];
  storedDigestsLoading: boolean;
  selectedDigestDate: string | null;
  onSelectDigest: (date: string) => void;
}) {
  const [refinementsOpen, setRefinementsOpen] = useState(false);
  const activeFiltersCount = (statusFilter !== null ? 1 : 0) + (sourceTypeFilter !== null ? 1 : 0);

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
    <div className="flex min-h-0 w-full flex-1 flex-col overflow-hidden border-r border-border bg-card/50">
      {/* Search + Filter Toolbar */}
      {!digestView && (
        <div className="shrink-0 border-b border-border px-3 py-2">
          <div className="flex items-center gap-2">
            <div className="relative flex-1">
              <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 w-3.5 h-3.5 text-muted-foreground/45 pointer-events-none" />
              <input
                type="text"
                value={searchText}
                onChange={(e) => setSearchText(e.target.value)}
                placeholder={t("recally.searchPlaceholder")}
                className="w-full pl-8 pr-3 py-1.5 text-xs font-mono rounded-md border border-border focus:border-primary focus:ring-1 focus:ring-primary focus:outline-none transition-colors duration-120 text-foreground placeholder:text-muted-foreground/45"
              />
            </div>
            <button
              onClick={() => setRefinementsOpen(!refinementsOpen)}
              title={t("recally.article.filterRefinements")}
              aria-label={t("recally.article.filterRefinements")}
              className={cn(
                "relative p-2 rounded-md border transition-colors duration-120 cursor-pointer flex items-center justify-center shrink-0",
                refinementsOpen || activeFiltersCount > 0
                  ? "bg-primary/10 text-primary border-primary/25"
                  : "bg-card border-border text-muted-foreground hover:text-foreground hover:bg-muted",
              )}
            >
              <SlidersHorizontal className="size-3.5" />
              {activeFiltersCount > 0 && (
                <span className="absolute -top-1 -right-1 flex h-4 w-4 items-center justify-center rounded-full bg-primary text-xs font-semibold text-primary-foreground">
                  {activeFiltersCount}
                </span>
              )}
            </button>
          </div>
        </div>
      )}

      {/* Quick Refinements */}
      {refinementsOpen && !digestView && (
        <div className="shrink-0 border-b border-border/40 p-3 space-y-2.5">
          <div className="flex items-center gap-3">
            <span className="text-xs font-mono font-semibold text-muted-foreground w-12 shrink-0">
              {t("recally.filter.status")}
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
          <div className="flex items-center gap-3">
            <span className="text-xs font-mono font-semibold text-muted-foreground w-12 shrink-0">
              {t("recally.filter.source")}
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

      {/* Article / Digest list */}
      <div className="flex-1 min-h-0 overflow-y-auto p-2 space-y-1">
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
                  "w-full rounded-lg border p-3 text-left transition-colors duration-120 cursor-pointer",
                  selectedDigestDate === d.date
                    ? "border-primary/30 bg-primary/5 text-foreground ring-1 ring-primary/20"
                    : "border-border bg-card hover:border-border/80 hover:bg-muted",
                )}
              >
                <div className="font-mono text-sm font-semibold text-foreground">{d.date}</div>
                <div className="mt-1 text-xs text-muted-foreground">
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
              <div className="flex items-center justify-center h-32 text-xs font-mono text-destructive-foreground">
                {t("common.error")}
              </div>
            )}
            {!articlesQuery.isLoading && !articlesQuery.isError && displayArticles.length === 0 && (
              <div className="flex flex-col items-center justify-center h-32 text-center">
                <p className="text-xs font-mono text-muted-foreground">
                  {t("recally.empty.noArticles")}
                </p>
                <p className="text-xs font-mono text-muted-foreground/45 mt-1">
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
