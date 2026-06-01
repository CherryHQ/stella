import { Sparkles, Star } from "lucide-react";
import type { Article } from "@/lib/api-client/types.gen";
import type { TFunction } from "../constants";
import { formatSavedAt, SOURCE_LABEL_KEYS } from "../constants";
import { StatusBadge } from "./StatusBadge";

export function ArticleCard({
  article,
  selected,
  onClick,
  t,
}: {
  article: Article;
  selected: boolean;
  onClick: () => void;
  t: TFunction;
}) {
  return (
    <button
      onClick={onClick}
      className={`w-full rounded-xl border p-3.5 text-left transition-all duration-200 ${
        selected
          ? "border-primary/20 bg-primary/[0.03] text-foreground shadow-xs ring-1 ring-primary/10"
          : "border-border/40 bg-card/45 hover:border-border/80 hover:bg-card/75 hover:scale-[1.01] hover:shadow-2xs"
      }`}
    >
      <div className="flex items-start justify-between gap-2.5">
        <h3 className="text-[12px] font-semibold leading-snug text-foreground">{article.title}</h3>
        {article.starred && (
          <Star className="size-3.5 text-amber-500 fill-amber-500 shrink-0 mt-0.5" />
        )}
      </div>
      {article.summary && (
        <div className="mt-2 flex items-start gap-1.5">
          <Sparkles className="mt-0.5 size-2.5 shrink-0 text-primary/60" />
          <p className="line-clamp-2 text-[11px] leading-relaxed text-muted-foreground/75">
            {article.summary}
          </p>
        </div>
      )}
      <div className="mt-3 flex flex-wrap items-center gap-1.5 font-mono text-[9px]">
        <StatusBadge status={article.status} t={t} />
        <span className="text-muted-foreground/50 border border-border/20 rounded bg-muted/20 px-1 py-0.5">
          {t(SOURCE_LABEL_KEYS[article.source_type])}
        </span>
        <span className="text-muted-foreground/55">{formatSavedAt(article.saved_at, t)}</span>
        {article.tags?.map((tag) => (
          <span
            key={tag}
            className="rounded-full bg-muted/30 border border-border/30 px-1.5 py-0.5 text-muted-foreground/70 hover:text-primary hover:border-primary/20 hover:bg-primary/5 transition-all"
          >
            {tag}
          </span>
        ))}
      </div>
    </button>
  );
}
