import { useState } from "react";
import { useI18n } from "@/lib/i18n";
import { X } from "lucide-react";
import { AppShell } from "@/layouts/AppShell";
import { useRecallyFilters } from "./hooks/useRecallyFilters";
import { useRecallyMutations } from "./hooks/useRecallyMutations";
import { useRecallyFeeds } from "./hooks/useRecallyFeeds";
import { RecallyArticleList } from "./components/RecallyArticleList";
import { DigestDetail } from "./components/DigestDetail";
import { RecallyReader } from "./components/RecallyReader";
import { RecallyChat } from "./components/RecallyChat";
import { ToastAlert } from "./components/ToastAlert";

function ArticleListProps(
  t: ReturnType<typeof useI18n>["t"],
  filters: ReturnType<typeof useRecallyFilters>,
  feeds: ReturnType<typeof useRecallyFeeds>,
  selectedId: string | null,
  setSelectedId: (id: string | null) => void,
  handleSelectDigest: (date: string) => void,
) {
  return {
    t,
    displayArticles: filters.displayArticles,
    articlesQuery: filters.articlesQuery,
    selectedId,
    setSelectedId,
    searchText: filters.searchText,
    setSearchText: filters.setSearchText,
    statusFilter: filters.statusFilter,
    setStatusFilter: filters.setStatusFilter,
    starredFilter: filters.starredFilter,
    setStarredFilter: filters.setStarredFilter,
    sourceTypeFilter: filters.sourceTypeFilter,
    setSourceTypeFilter: filters.setSourceTypeFilter,
    tagFilter: filters.tagFilter,
    setTagFilter: filters.setTagFilter,
    digest: filters.digest,
    digestView: filters.digestView,
    setDigestView: filters.setDigestView,
    storedDigests: filters.storedDigests,
    storedDigestsLoading: filters.storedDigestsQuery.isLoading,
    selectedDigestDate: filters.selectedDigestDate,
    onSelectDigest: handleSelectDigest,
    clearFilters: filters.clearFilters,
    sortedTags: filters.sortedTags,
    visibleTags: filters.visibleTags,
    showAllTags: filters.showAllTags,
    setShowAllTags: filters.setShowAllTags,
    tagCounts: filters.tagCounts,
    feeds: feeds.feeds,
    feedUrl: feeds.feedUrl,
    setFeedUrl: feeds.setFeedUrl,
    createFeedMut: feeds.createFeedMut,
    pollFeedMut: feeds.pollFeedMut,
    feedPollResults: feeds.feedPollResults,
  };
}

export function RecallyPage() {
  const { t } = useI18n();
  const [selectedId, setSelectedId] = useState<string | null>(null);
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

  const listProps = ArticleListProps(
    t,
    filters,
    feeds,
    selectedId,
    setSelectedId,
    handleSelectDigest,
  );

  const headerTitle = (
    <h1 className="truncate text-sm font-semibold tracking-[-0.01em] text-foreground">
      {showDigestDetail
        ? `${t("recally.nav.digest")} — ${filters.selectedDigestDate}`
        : t("recally.title")}
    </h1>
  );

  const mobileOverlayActive = !!(selectedId || showDigestDetail);
  const headerActions = mobileOverlayActive ? (
    <button
      type="button"
      onClick={() => {
        if (chatOpen) setChatOpen(false);
        else if (selectedId) setSelectedId(null);
        else filters.setSelectedDigestDate(null);
      }}
      className="rounded-md p-1.5 text-muted-foreground transition-colors hover:bg-accent hover:text-foreground cursor-pointer md:hidden"
    >
      <X className="size-4" />
    </button>
  ) : undefined;

  return (
    <AppShell
      sidebar={<RecallyArticleList {...listProps} />}
      title={headerTitle}
      headerActions={headerActions}
    >
      <div className="flex h-full min-h-0 overflow-hidden">
        <div className="flex-1 min-h-0 overflow-y-auto">
          {showDigestDetail ? (
            <DigestDetail t={t} digest={filters.selectedDigest!} onSelectArticle={setSelectedId} />
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
          <aside className="hidden w-[340px] shrink-0 border-l border-border md:flex">
            <RecallyChat articleId={selectedId} onClose={() => setChatOpen(false)} />
          </aside>
        )}
      </div>

      {/* Mobile digest detail overlay */}
      {showDigestDetail && (
        <div className="fixed inset-x-0 bottom-0 top-12 z-40 flex flex-col bg-background md:hidden">
          <DigestDetail t={t} digest={filters.selectedDigest!} onSelectArticle={setSelectedId} />
        </div>
      )}

      {/* Mobile reader / Chat overlay */}
      {selectedId && (
        <div className="fixed inset-x-0 bottom-0 top-12 z-50 flex flex-col bg-background md:hidden">
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
