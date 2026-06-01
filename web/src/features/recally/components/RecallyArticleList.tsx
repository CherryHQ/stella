import { type Dispatch, type SetStateAction } from "react";
import { Search, PanelLeftOpen, PanelLeftClose } from "lucide-react";
import { Button } from "@/components/ui/button";
import { useSidebar } from "@/components/ui/sidebar";
import type { Article, StoredDigestSummary } from "@/lib/api-client/types.gen";
import type { TFunction } from "../constants";
import { ArticleCard } from "./ArticleCard";
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

  // Digest View Integration
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

  digestView: boolean;
  storedDigests: StoredDigestSummary[];
  storedDigestsLoading: boolean;
  selectedDigestDate: string | null;
  onSelectDigest: (date: string) => void;
}) {
  const { state: sidebarState, toggleSidebar } = useSidebar();

  return (
    <section className="flex min-h-0 flex-1 flex-col overflow-hidden border-r border-border bg-card/40">
      {/* Segmented Control / Tabs */}
      <div className="shrink-0 border-b border-border bg-card/65 px-4 pt-3 pb-2 backdrop-blur-xl">
        <div className="flex items-center justify-between">
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

        {/* Search Bar */}
        <div className="relative mt-2">
          <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 w-3.5 h-3.5 text-muted-foreground/45 pointer-events-none" />
          <input
            type="text"
            value={searchText}
            onChange={(e) => setSearchText(e.target.value)}
            placeholder={t("recally.searchPlaceholder")}
            className="w-full pl-8 pr-3 py-1.5 text-[11px] font-mono rounded-lg bg-muted/20 border border-border/40 hover:border-border/75 focus:border-primary/40 focus:ring-2 focus:ring-primary/5 focus:outline-none transition-all duration-150 text-foreground placeholder:text-muted-foreground/40 shadow-2xs"
          />
        </div>
      </div>

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
