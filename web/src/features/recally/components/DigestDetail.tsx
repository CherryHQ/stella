import { Star } from "lucide-react";
import type { StoredDigest, Article } from "@/lib/api-client/types.gen";
import type { TFunction } from "../constants";

export function DigestDetail({
  t,
  digest,
  onSelectArticle,
}: {
  t: TFunction;
  digest: StoredDigest;
  onSelectArticle: (id: string) => void;
}) {
  return (
    <div className="flex-1 overflow-auto p-4 bg-background">
      <div className="space-y-5">
        <h2 className="font-mono text-base font-semibold text-foreground tracking-tight">
          {digest.date}
        </h2>

        {digest.narrative && (
          <div className="rounded-xl border border-border bg-card p-4 text-sm leading-relaxed text-foreground whitespace-pre-line">
            {digest.narrative}
          </div>
        )}

        {digest.saved_yesterday?.length > 0 && (
          <ArticleGroup
            title={t("recally.digest.savedYesterdaySection")}
            articles={digest.saved_yesterday}
            onSelectArticle={onSelectArticle}
          />
        )}

        {digest.worth_revisiting?.length > 0 && (
          <ArticleGroup
            title={t("recally.digest.worthRevisitingSection")}
            articles={digest.worth_revisiting}
            onSelectArticle={onSelectArticle}
          />
        )}

        {digest.top_tags?.length > 0 && (
          <div>
            <SectionLabel title={t("recally.digest.topTagsSection")} />
            <div className="mt-2 flex flex-wrap gap-1.5">
              {digest.top_tags.map(({ tag, count }) => (
                <span
                  key={tag}
                  className="rounded-full border border-border bg-card px-2.5 py-0.5 text-xs font-mono text-muted-foreground font-medium"
                >
                  {tag} <span className="text-muted-foreground">{count}</span>
                </span>
              ))}
            </div>
          </div>
        )}
      </div>
    </div>
  );
}

function SectionLabel({ title }: { title: string }) {
  return <div className="text-xs font-mono font-semibold text-muted-foreground">{title}</div>;
}

function ArticleGroup({
  title,
  articles,
  onSelectArticle,
}: {
  title: string;
  articles: Article[];
  onSelectArticle: (id: string) => void;
}) {
  return (
    <div>
      <SectionLabel title={title} />
      <ul className="mt-2 space-y-1">
        {articles.map((a) => (
          <li key={a.id}>
            <button
              type="button"
              onClick={() => onSelectArticle(a.id)}
              className="group flex w-full items-start gap-2 rounded-lg px-2.5 py-2 text-left transition-colors duration-120 hover:bg-muted cursor-pointer"
            >
              {a.starred && <Star className="mt-0.5 size-3 shrink-0 fill-warning text-warning" />}
              <span className="line-clamp-2 text-sm leading-snug text-foreground">{a.title}</span>
            </button>
          </li>
        ))}
      </ul>
    </div>
  );
}
