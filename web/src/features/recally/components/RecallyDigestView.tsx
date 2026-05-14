import { cn } from "@/lib/utils";
import type { StoredDigest, StoredDigestSummary, Article } from "@/lib/api-client/types.gen";
import type { TFunction } from "../constants";
import { StatCard } from "./StatCard";
import { ArticleCard } from "./ArticleCard";

export function RecallyDigestView({
  t,
  className,
  storedDigests,
  storedDigestsLoading,
  selectedDigestDate,
  setSelectedDigestDate,
  selectedDigest,
  selectedDigestLoading,
  selectedId,
  setSelectedId,
}: {
  t: TFunction;
  className?: string;
  storedDigests: StoredDigestSummary[];
  storedDigestsLoading: boolean;
  selectedDigestDate: string | null;
  setSelectedDigestDate: (date: string | null) => void;
  selectedDigest: StoredDigest | undefined;
  selectedDigestLoading: boolean;
  selectedId: string | null;
  setSelectedId: (id: string | null) => void;
}) {
  return (
    <section
      className={cn(
        "flex min-h-0 flex-col overflow-hidden border-r border-border bg-background",
        className,
      )}
    >
      <div className="shrink-0 border-b border-border bg-background px-4 py-4">
        <h1 className="text-xl font-semibold tracking-tight text-foreground">
          {t("recally.nav.digest")}
        </h1>
      </div>

      <div className="flex min-h-0 flex-1 overflow-hidden">
        {/* History list */}
        <div className="flex w-48 shrink-0 flex-col overflow-auto border-r border-border bg-muted/20">
          {storedDigestsLoading && (
            <p className="px-3 py-4 text-xs text-muted-foreground">{t("common.loading")}</p>
          )}
          {!storedDigestsLoading && storedDigests.length === 0 && (
            <p className="px-3 py-4 text-xs text-muted-foreground">
              {t("recally.digest.noHistory")}
            </p>
          )}
          {storedDigests.map((d) => (
            <button
              key={d.id}
              type="button"
              onClick={() => setSelectedDigestDate(d.date)}
              className={`w-full px-3 py-2.5 text-left transition-colors hover:bg-accent/50 ${
                selectedDigestDate === d.date ? "bg-accent" : ""
              }`}
            >
              <div className="font-mono text-xs font-medium text-foreground">{d.date}</div>
              <div className="mt-0.5 text-[10px] text-muted-foreground">
                {d.saved_yesterday_count} saved · {d.worth_revisiting_count} revisit
              </div>
            </button>
          ))}
        </div>

        {/* Detail panel */}
        <div className="flex-1 overflow-auto p-4">
          {!selectedDigestDate && (
            <p className="text-sm text-muted-foreground">{t("recally.digest.selectPrompt")}</p>
          )}
          {selectedDigestLoading && (
            <p className="text-sm text-muted-foreground">{t("common.loading")}</p>
          )}
          {selectedDigest && (
            <DigestDetail
              t={t}
              digest={selectedDigest}
              selectedId={selectedId}
              setSelectedId={setSelectedId}
            />
          )}
        </div>
      </div>
    </section>
  );
}

function DigestDetail({
  t,
  digest,
  selectedId,
  setSelectedId,
}: {
  t: TFunction;
  digest: StoredDigest;
  selectedId: string | null;
  setSelectedId: (id: string | null) => void;
}) {
  return (
    <div className="space-y-6">
      {/* Narrative */}
      {digest.narrative && (
        <div className="rounded-md border border-border bg-card p-3">
          <pre className="whitespace-pre-wrap font-sans text-sm leading-relaxed text-foreground">
            {digest.narrative}
          </pre>
        </div>
      )}

      {/* Stats */}
      <div className="grid grid-cols-2 gap-2 lg:grid-cols-4">
        <StatCard value={digest.total_articles} label={t("recally.stat.total")} />
        <StatCard value={digest.unread_count} label={t("recally.stat.unread")} />
        <StatCard value={digest.saved_yesterday_count} label={t("recally.stat.savedYesterday")} />
        <StatCard value={digest.worth_revisiting_count} label={t("recally.stat.worthRevisiting")} />
      </div>

      {/* Article sections */}
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

      {/* Top tags */}
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
    </div>
  );
}

function SectionHeader({ title }: { title: string }) {
  return (
    <div className="text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
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
          <p className="py-2 text-sm text-muted-foreground">{emptyText}</p>
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
