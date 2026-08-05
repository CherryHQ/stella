import { useCallback, useState } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { useI18n } from "@/lib/i18n";
import { X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { AppShell } from "@/layouts/AppShell";
import { useRecallyFilters } from "./hooks/useRecallyFilters";
import { useRecallyMutations } from "./hooks/useRecallyMutations";
import { useRecallyFeeds } from "./hooks/useRecallyFeeds";
import { RecallySourceNav } from "./components/RecallySourceNav";
import { RecallyArticleList } from "./components/RecallyArticleList";
import { DigestDetail } from "./components/DigestDetail";
import { RecallyReader } from "./components/RecallyReader";
import { RecallyChat } from "./components/RecallyChat";
import { ToastAlert } from "./components/ToastAlert";

export function RecallyPage() {
  const { t } = useI18n();
  // Selected article lives in the URL (`?article=`) so it survives refresh and
  // back/forward, and so reference cards can deep-link straight to a reader.
  const navigate = useNavigate();
  const { article } = useSearch({ from: "/_app/recally" });
  const selectedId = article ?? null;
  const setSelectedId = useCallback(
    (id: string | null) => {
      void navigate({
        to: "/recally",
        search: (prev) => ({ ...prev, article: id ?? undefined }),
      });
    },
    [navigate],
  );
  const [chatOpen, setChatOpen] = useState(false);

  const filters = useRecallyFilters();
  const mutations = useRecallyMutations(selectedId, setSelectedId);
  const feeds = useRecallyFeeds(mutations.showToast);

  function handleSelectDigest(date: string) {
    filters.setSelectedDigestDate(date);
    setSelectedId(null);
  }

  const showDigestDetail =
    filters.digestView && !!filters.selectedDigestDate && !selectedId && !!filters.selectedDigest;

  const selectInbox = () => {
    filters.setDigestView(false);
    filters.clearFilters();
  };
  const selectStarred = () => {
    filters.setDigestView(false);
    filters.setStarredFilter(true);
    filters.setStatusFilter(null);
    filters.setSourceTypeFilter(null);
    filters.setTagFilter(null);
  };
  const selectArchive = () => {
    filters.setDigestView(false);
    filters.setStatusFilter("archived");
    filters.setStarredFilter(null);
    filters.setSourceTypeFilter(null);
    filters.setTagFilter(null);
  };
  const selectDigest = () => {
    filters.setDigestView(true);
    filters.setStatusFilter(null);
    filters.setStarredFilter(null);
    filters.setSourceTypeFilter(null);
    filters.setTagFilter(null);
  };
  const selectTag = (tag: string | null) => {
    filters.setTagFilter(tag);
    filters.setDigestView(false);
    filters.setStarredFilter(null);
    filters.setStatusFilter(null);
    filters.setSourceTypeFilter(null);
  };

  const headerTitle = (
    <h1 className="truncate text-sm font-semibold tracking-[-0.01em] text-foreground">
      {showDigestDetail
        ? `${t("recally.nav.digest")} — ${filters.selectedDigestDate}`
        : t("recally.title")}
    </h1>
  );

  const mobileOverlayActive = !!(selectedId || showDigestDetail);
  const headerActions = mobileOverlayActive ? (
    <Button
      type="button"
      variant="ghost"
      size="icon-xs"
      onClick={() => {
        if (chatOpen) setChatOpen(false);
        else if (selectedId) setSelectedId(null);
        else filters.setSelectedDigestDate(null);
      }}
      className="text-muted-foreground hover:text-foreground md:hidden"
    >
      <X className="size-4" />
    </Button>
  ) : undefined;

  const sourceNav = (
    <RecallySourceNav
      t={t}
      digest={filters.digest}
      digestView={filters.digestView}
      starredFilter={filters.starredFilter}
      statusFilter={filters.statusFilter}
      tagFilter={filters.tagFilter}
      selectInbox={selectInbox}
      selectStarred={selectStarred}
      selectArchive={selectArchive}
      selectDigest={selectDigest}
      selectTag={selectTag}
      sortedTags={filters.sortedTags}
      visibleTags={filters.visibleTags}
      showAllTags={filters.showAllTags}
      setShowAllTags={filters.setShowAllTags}
      tagCounts={filters.tagCounts}
      feeds={feeds.feeds}
      feedUrl={feeds.feedUrl}
      setFeedUrl={feeds.setFeedUrl}
      createFeedMut={feeds.createFeedMut}
      pollFeedMut={feeds.pollFeedMut}
      feedPollResults={feeds.feedPollResults}
    />
  );

  return (
    <AppShell sidebar={sourceNav} title={headerTitle} headerActions={headerActions}>
      {/* Desktop: three-column layout */}
      <div className="flex h-full min-h-0 overflow-hidden">
        {/* Middle: Article list — hidden on mobile when reader is open */}
        <div className="hidden w-80 shrink-0 md:flex md:flex-col lg:w-96">
          <RecallyArticleList
            t={t}
            displayArticles={filters.displayArticles}
            articlesQuery={filters.articlesQuery}
            selectedId={selectedId}
            setSelectedId={setSelectedId}
            searchText={filters.searchText}
            setSearchText={filters.setSearchText}
            statusFilter={filters.statusFilter}
            setStatusFilter={filters.setStatusFilter}
            sourceTypeFilter={filters.sourceTypeFilter}
            setSourceTypeFilter={filters.setSourceTypeFilter}
            digestView={filters.digestView}
            storedDigests={filters.storedDigests}
            storedDigestsLoading={filters.storedDigestsQuery.isLoading}
            selectedDigestDate={filters.selectedDigestDate}
            onSelectDigest={handleSelectDigest}
          />
        </div>

        {/* Right: Reader / Digest detail */}
        <div className="hidden flex-1 min-h-0 min-w-0 md:flex md:flex-col">
          <div className="flex h-full min-h-0">
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
            {chatOpen && selectedId && !showDigestDetail && (
              <aside className="w-[340px] shrink-0 border-l border-border flex">
                <RecallyChat articleId={selectedId} onClose={() => setChatOpen(false)} />
              </aside>
            )}
          </div>
        </div>

        {/* Mobile: article list is shown inline */}
        <div className="flex flex-1 flex-col min-h-0 md:hidden">
          <RecallyArticleList
            t={t}
            displayArticles={filters.displayArticles}
            articlesQuery={filters.articlesQuery}
            selectedId={selectedId}
            setSelectedId={setSelectedId}
            searchText={filters.searchText}
            setSearchText={filters.setSearchText}
            statusFilter={filters.statusFilter}
            setStatusFilter={filters.setStatusFilter}
            sourceTypeFilter={filters.sourceTypeFilter}
            setSourceTypeFilter={filters.setSourceTypeFilter}
            digestView={filters.digestView}
            storedDigests={filters.storedDigests}
            storedDigestsLoading={filters.storedDigestsQuery.isLoading}
            selectedDigestDate={filters.selectedDigestDate}
            onSelectDigest={handleSelectDigest}
          />
        </div>
      </div>

      {/* Mobile digest detail overlay */}
      {showDigestDetail && (
        <div className="fixed inset-x-0 bottom-0 top-14 z-40 flex flex-col bg-background md:hidden">
          <DigestDetail t={t} digest={filters.selectedDigest!} onSelectArticle={setSelectedId} />
        </div>
      )}

      {/* Mobile reader / Chat overlay */}
      {selectedId && (
        <div className="fixed inset-x-0 bottom-0 top-14 z-50 flex flex-col bg-background md:hidden">
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
    </AppShell>
  );
}
