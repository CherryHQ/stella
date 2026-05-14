import type { Digest, Article } from "@/lib/api-client/types.gen";
import type { TFunction } from "../constants";
import { StatCard } from "./StatCard";
import { ArticleCard } from "./ArticleCard";

export function RecallyDigestView({
  t,
  digest,
  digestLoading,
  digestError,
  selectedId,
  setSelectedId,
}: {
  t: TFunction;
  digest: Digest | undefined;
  digestLoading: boolean;
  digestError: boolean;
  selectedId: string | null;
  setSelectedId: (id: string | null) => void;
}) {
  return (
    <section className="flex min-h-0 flex-col overflow-hidden border-r border-border bg-background">
      <div className="shrink-0 border-b border-border bg-background px-4 py-4">
        <h1 className="text-xl font-semibold tracking-tight text-foreground">
          {t("recally.nav.digest")}
        </h1>
        {digest && (
          <div className="mt-3 grid grid-cols-2 gap-2 lg:grid-cols-4">
            <StatCard value={digest.total_articles} label={t("recally.stat.total")} />
            <StatCard value={digest.unread_count} label={t("recally.stat.unread")} />
            <StatCard value={digest.read_count} label={t("recally.stat.read")} />
            <StatCard value={digest.archived_count} label={t("recally.stat.archived")} />
            <StatCard value={digest.starred_count} label={t("recally.stat.starred")} />
            <StatCard
              value={digest.saved_yesterday_count}
              label={t("recally.stat.savedYesterday")}
            />
            <StatCard
              value={digest.worth_revisiting_count}
              label={t("recally.stat.worthRevisiting")}
            />
          </div>
        )}
      </div>

      <div className="flex-1 overflow-auto p-3 space-y-6">
        {digestLoading && (
          <div className="flex items-center justify-center h-32 text-sm text-muted-foreground">
            {t("common.loading")}
          </div>
        )}
        {digestError && (
          <div className="flex items-center justify-center h-32 text-sm text-destructive">
            {t("common.error")}
          </div>
        )}
        {digest && (
          <>
            <ArticleSection
              title={t("recally.digest.savedYesterdaySection")}
              articles={digest.saved_yesterday}
              emptyText={t("recally.digest.noSavedYesterday")}
              selectedId={selectedId}
              setSelectedId={setSelectedId}
              t={t}
            />
            <ArticleSection
              title={t("recally.digest.worthRevisitingSection")}
              articles={digest.worth_revisiting}
              emptyText={t("recally.digest.noWorthRevisiting")}
              selectedId={selectedId}
              setSelectedId={setSelectedId}
              t={t}
            />
            {digest.top_tags.length > 0 && (
              <div>
                <SectionHeader title={t("recally.digest.topTagsSection")} />
                <div className="mt-2 flex flex-wrap gap-1.5">
                  {digest.top_tags.map(({ tag, count }) => (
                    <span
                      key={tag}
                      className="rounded-full border border-border bg-card px-2.5 py-1 text-xs font-medium"
                    >
                      {tag} <span className="text-muted-foreground">{count}</span>
                    </span>
                  ))}
                </div>
              </div>
            )}
          </>
        )}
      </div>
    </section>
  );
}

function SectionHeader({ title }: { title: string }) {
  return (
    <div className="px-1 text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
      {title}
    </div>
  );
}

function ArticleSection({
  title,
  articles,
  emptyText,
  selectedId,
  setSelectedId,
  t,
}: {
  title: string;
  articles: Article[];
  emptyText: string;
  selectedId: string | null;
  setSelectedId: (id: string | null) => void;
  t: TFunction;
}) {
  return (
    <div>
      <SectionHeader title={title} />
      <div className="mt-2 space-y-1">
        {articles.length === 0 ? (
          <p className="px-1 py-2 text-sm text-muted-foreground">{emptyText}</p>
        ) : (
          articles.map((article) => (
            <ArticleCard
              key={article.id}
              article={article}
              selected={selectedId === article.id}
              onClick={() => setSelectedId(article.id)}
              t={t}
            />
          ))
        )}
      </div>
    </div>
  );
}
