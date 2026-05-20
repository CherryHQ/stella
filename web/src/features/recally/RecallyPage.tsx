import { useState, type CSSProperties } from "react";
import { cn } from "@/lib/utils";
import { useI18n } from "@/lib/i18n";
import { X } from "lucide-react";
import { CENTER_WIDTH_DEFAULT, CENTER_WIDTH_MIN, CENTER_WIDTH_MAX } from "./constants";
import { useRecallyFilters } from "./hooks/useRecallyFilters";
import { useRecallyMutations } from "./hooks/useRecallyMutations";
import { useRecallyFeeds } from "./hooks/useRecallyFeeds";
import { RecallySidebar } from "./components/RecallySidebar";
import { RecallyArticleList } from "./components/RecallyArticleList";
import { RecallyDigestView } from "./components/RecallyDigestView";
import { DigestDetail } from "./components/DigestDetail";
import { RecallyReader } from "./components/RecallyReader";
import { AppSidebar } from "@/components/AppSidebar";
import { ToastAlert } from "./components/ToastAlert";

export function RecallyPage() {
  const { t } = useI18n();
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [centerWidth, setCenterWidth] = useState(CENTER_WIDTH_DEFAULT);

  const filters = useRecallyFilters();
  const mutations = useRecallyMutations(selectedId, setSelectedId);
  const feeds = useRecallyFeeds(mutations.showToast);

  function handleSelectDigest(date: string) {
    filters.setSelectedDigestDate(date);
    setSelectedId(null);
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

  const showDigestDetail =
    filters.digestView && !!filters.selectedDigestDate && !selectedId && !!filters.selectedDigest;

  return (
    <div className="relative flex h-full min-h-0 overflow-hidden bg-background">
      {/* Left sidebar */}
      <aside
        className={cn(
          "hidden md:flex flex-shrink-0 flex-col overflow-hidden border-r border-border bg-sidebar transition-all duration-200 ease-out",
          filters.leftOpen
            ? "w-[260px] min-w-[260px]"
            : "w-0 min-w-0 overflow-hidden opacity-0 pointer-events-none",
        )}
      >
        <AppSidebar>
          <div className="min-h-0 flex-1 overflow-y-auto">
            <RecallySidebar
              t={t}
              searchText={filters.searchText}
              setSearchText={filters.setSearchText}
              statusFilter={filters.statusFilter}
              setStatusFilter={filters.setStatusFilter}
              sourceTypeFilter={filters.sourceTypeFilter}
              setSourceTypeFilter={filters.setSourceTypeFilter}
              starredFilter={filters.starredFilter}
              setStarredFilter={filters.setStarredFilter}
              tagFilter={filters.tagFilter}
              setTagFilter={filters.setTagFilter}
              showAllTags={filters.showAllTags}
              setShowAllTags={filters.setShowAllTags}
              digest={filters.digest}
              digestView={filters.digestView}
              setDigestView={filters.setDigestView}
              sortedTags={filters.sortedTags}
              visibleTags={filters.visibleTags}
              hasMoreTags={filters.hasMoreTags}
              tagCounts={filters.tagCounts}
              feeds={feeds.feeds}
              feedsQuery={feeds.feedsQuery}
              feedUrl={feeds.feedUrl}
              setFeedUrl={feeds.setFeedUrl}
              createFeedMut={feeds.createFeedMut}
              pollFeedMut={feeds.pollFeedMut}
              feedPollResults={feeds.feedPollResults}
              clearFilters={filters.clearFilters}
            />
          </div>
        </AppSidebar>
      </aside>

      <button
        type="button"
        onClick={() => filters.setLeftOpen((v) => !v)}
        className={cn(
          "absolute top-3 z-20 hidden h-[34px] w-[18px] place-items-center rounded-full border border-border/60 bg-card text-muted-foreground/50 shadow-sm transition-all duration-200 hover:bg-accent hover:text-foreground md:grid",
          filters.leftOpen ? "left-[250px]" : "-left-[9px]",
        )}
        aria-label={filters.leftOpen ? "Hide sidebar" : "Show sidebar"}
      >
        <svg
          className={cn("size-3 transition-transform", !filters.leftOpen && "rotate-180")}
          viewBox="0 0 16 16"
          fill="none"
          stroke="currentColor"
          strokeWidth="2"
        >
          <path d="m10 4-4 4 4 4" />
        </svg>
      </button>

      {/* Main area */}
      <div
        className="min-w-0 flex-1 grid grid-cols-1 overflow-hidden xl:grid-cols-[var(--recally-center-width)_1fr]"
        style={{ "--recally-center-width": `${centerWidth}px` } as CSSProperties}
      >
        {/* Center panel */}
        {filters.digestView ? (
          <RecallyDigestView
            t={t}
            storedDigests={filters.storedDigests}
            storedDigestsLoading={filters.storedDigestsQuery.isLoading}
            selectedDigestDate={filters.selectedDigestDate}
            onSelectDigest={handleSelectDigest}
          />
        ) : (
          <RecallyArticleList
            t={t}
            displayArticles={filters.displayArticles}
            articlesQuery={filters.articlesQuery}
            selectedId={selectedId}
            setSelectedId={setSelectedId}
            digest={filters.digest}
            searchText={filters.searchText}
            setSearchText={filters.setSearchText}
            statusFilter={filters.statusFilter}
            setStatusFilter={filters.setStatusFilter}
          />
        )}

        {/* Right reader panel — always in DOM */}
        <aside className="relative hidden min-h-0 flex-col bg-background xl:flex">
          <button
            type="button"
            aria-label={t("recally.resizeList")}
            onMouseDown={startResize}
            className="absolute inset-y-0 left-0 z-10 w-2 -translate-x-1 cursor-col-resize border-l border-border/70 transition-colors hover:bg-accent"
          />
          {showDigestDetail ? (
            <DigestDetail t={t} digest={filters.selectedDigest!} onSelectArticle={setSelectedId} />
          ) : (
            <RecallyReader
              t={t}
              selectedId={selectedId}
              updateArticleMut={mutations.updateArticleMut}
              deleteArticleMut={mutations.deleteArticleMut}
            />
          )}
        </aside>
      </div>

      {/* Mobile digest detail overlay */}
      {showDigestDetail && (
        <div className="fixed inset-x-0 bottom-0 top-14 z-40 flex flex-col border-t border-border bg-background shadow-lg xl:hidden">
          <div className="flex h-11 shrink-0 items-center justify-between border-b border-border px-3">
            <span className="text-sm font-medium">{t("recally.nav.digest")}</span>
            <button
              type="button"
              onClick={() => filters.setSelectedDigestDate(null)}
              className="rounded-md p-1.5 text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
            >
              <X className="size-4" />
            </button>
          </div>
          <DigestDetail t={t} digest={filters.selectedDigest!} onSelectArticle={setSelectedId} />
        </div>
      )}

      {/* Mobile reader overlay */}
      {selectedId && (
        <div className="fixed inset-x-0 bottom-0 top-14 z-50 flex flex-col border-t border-border bg-background shadow-lg xl:hidden">
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
          <RecallyReader
            t={t}
            selectedId={selectedId}
            updateArticleMut={mutations.updateArticleMut}
            deleteArticleMut={mutations.deleteArticleMut}
          />
        </div>
      )}

      <ToastAlert toast={mutations.toast} />
    </div>
  );
}
