import { useState, type CSSProperties } from "react";
import { useI18n } from "@/lib/i18n";
import { X, PanelLeftOpen, PanelLeftClose } from "lucide-react";
import { Button } from "@/components/ui/button";
import { CENTER_WIDTH_DEFAULT, CENTER_WIDTH_MIN, CENTER_WIDTH_MAX } from "./constants";
import { useRecallyFilters } from "./hooks/useRecallyFilters";
import { useRecallyMutations } from "./hooks/useRecallyMutations";
import { useRecallyFeeds } from "./hooks/useRecallyFeeds";
import { RecallyArticleList } from "./components/RecallyArticleList";
import { DigestDetail } from "./components/DigestDetail";
import { RecallyReader } from "./components/RecallyReader";
import { RecallyChat } from "./components/RecallyChat";
import { ToastAlert } from "./components/ToastAlert";
import { cn } from "@/lib/utils";

export function RecallyPage() {
  const { t } = useI18n();
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [centerWidth, setCenterWidth] = useState(CENTER_WIDTH_DEFAULT);
  const [chatOpen, setChatOpen] = useState(false);
  const [sidebarOpen, setSidebarOpen] = useState(true);

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
    <div className="relative flex h-full min-h-0 overflow-hidden">
      {/* Grid layout: List | Reader | Chat */}
      <div
        className={cn(
          "min-w-0 flex-1 grid grid-cols-1 overflow-hidden transition-all duration-200",
          chatOpen && selectedId && !showDigestDetail
            ? "xl:grid-cols-[var(--recally-center-width)_1fr_340px]"
            : "xl:grid-cols-[var(--recally-center-width)_1fr]",
        )}
        style={{ "--recally-center-width": `${sidebarOpen ? centerWidth : 0}px` } as CSSProperties}
      >
        {/* Column 1: Unified sidebar + reading list */}
        <RecallyArticleList
          t={t}
          displayArticles={filters.displayArticles}
          articlesQuery={filters.articlesQuery}
          selectedId={selectedId}
          setSelectedId={setSelectedId}
          sidebarOpen={sidebarOpen}
          // Filters states
          searchText={filters.searchText}
          setSearchText={filters.setSearchText}
          statusFilter={filters.statusFilter}
          setStatusFilter={filters.setStatusFilter}
          starredFilter={filters.starredFilter}
          setStarredFilter={filters.setStarredFilter}
          sourceTypeFilter={filters.sourceTypeFilter}
          setSourceTypeFilter={filters.setSourceTypeFilter}
          tagFilter={filters.tagFilter}
          setTagFilter={filters.setTagFilter}
          // Stored digests / history view
          digest={filters.digest}
          digestView={filters.digestView}
          setDigestView={filters.setDigestView}
          storedDigests={filters.storedDigests}
          storedDigestsLoading={filters.storedDigestsQuery.isLoading}
          selectedDigestDate={filters.selectedDigestDate}
          onSelectDigest={handleSelectDigest}
          clearFilters={filters.clearFilters}
          // Tags configuration
          sortedTags={filters.sortedTags}
          visibleTags={filters.visibleTags}
          showAllTags={filters.showAllTags}
          setShowAllTags={filters.setShowAllTags}
          tagCounts={filters.tagCounts}
          // Feeds operations
          feeds={feeds.feeds}
          feedUrl={feeds.feedUrl}
          setFeedUrl={feeds.setFeedUrl}
          createFeedMut={feeds.createFeedMut}
          pollFeedMut={feeds.pollFeedMut}
          feedPollResults={feeds.feedPollResults}
        />

        {/* Column 2: Article Reader Panel */}
        <aside className="relative hidden min-h-0 flex-col bg-background xl:flex">
          <button
            type="button"
            aria-label={t("recally.resizeList")}
            onMouseDown={startResize}
            className="absolute inset-y-0 left-0 z-10 w-2 -translate-x-1 cursor-col-resize border-l border-border/70 transition-colors hover:bg-accent"
          />

          {/* Sticky Top Header Bar */}
          <div className="flex h-12 flex-shrink-0 items-center border-b border-border/70 bg-card/65 px-4 backdrop-blur-xl">
            <div className="flex items-center gap-2.5 w-full min-w-0">
              <Button
                variant="ghost"
                size="xs"
                onClick={() => setSidebarOpen(!sidebarOpen)}
                className="h-7 w-7 shrink-0 rounded-full p-0 text-muted-foreground inline-flex"
                title={!sidebarOpen ? "Show sidebar" : "Hide sidebar"}
              >
                {!sidebarOpen ? (
                  <PanelLeftOpen className="size-3.5" />
                ) : (
                  <PanelLeftClose className="size-3.5" />
                )}
              </Button>
              <div className="min-w-0 flex-1">
                <h1 className="truncate text-sm font-semibold tracking-[-0.01em] text-foreground/80">
                  {showDigestDetail
                    ? `${t("recally.nav.digest")} — ${filters.selectedDigestDate}`
                    : t("recally.title")}
                </h1>
              </div>
            </div>
          </div>

          <div className="flex-1 min-h-0 overflow-y-auto">
            {showDigestDetail ? (
              <DigestDetail
                t={t}
                digest={filters.selectedDigest!}
                onSelectArticle={setSelectedId}
              />
            ) : (
              <RecallyReader
                t={t}
                selectedId={selectedId}
                chatOpen={chatOpen}
                onToggleChat={() => setChatOpen(!chatOpen)}
                updateArticleMut={mutations.updateArticleMut}
                deleteArticleMut={mutations.deleteArticleMut}
              />
            )}
          </div>
        </aside>

        {/* Column 3: AI Chat Workspace */}
        {chatOpen && selectedId && !showDigestDetail && (
          <aside className="relative hidden min-h-0 flex-col xl:flex shrink-0">
            <RecallyChat articleId={selectedId} onClose={() => setChatOpen(false)} />
          </aside>
        )}
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

      {/* Mobile reader / Chat overlay */}
      {selectedId && (
        <div className="fixed inset-x-0 bottom-0 top-14 z-50 flex flex-col border-t border-border bg-background shadow-lg xl:hidden">
          <div className="flex h-11 shrink-0 items-center justify-between border-b border-border px-3.5 bg-card">
            <span className="text-xs font-semibold text-foreground">
              {chatOpen ? t("AI 边读边问" as any) || "AI Chat" : t("recally.title")}
            </span>
            <button
              type="button"
              onClick={() => {
                if (chatOpen) setChatOpen(false);
                else setSelectedId(null);
              }}
              className="rounded-md p-1.5 text-muted-foreground transition-colors hover:bg-accent hover:text-foreground cursor-pointer"
            >
              <X className="size-4" />
            </button>
          </div>
          {chatOpen ? (
            <div className="flex-1 min-h-0 flex flex-col w-full bg-background">
              <RecallyChat articleId={selectedId} onClose={() => setChatOpen(false)} />
            </div>
          ) : (
            <RecallyReader
              t={t}
              selectedId={selectedId}
              chatOpen={chatOpen}
              onToggleChat={() => setChatOpen(!chatOpen)}
              updateArticleMut={mutations.updateArticleMut}
              deleteArticleMut={mutations.deleteArticleMut}
            />
          )}
        </div>
      )}

      <ToastAlert toast={mutations.toast} />
    </div>
  );
}
