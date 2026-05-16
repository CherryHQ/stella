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
      className={`w-full rounded-lg p-3 text-left transition-all duration-150 ${
        selected ? "bg-sidebar-accent" : "hover:bg-muted/50"
      }`}
    >
      <div className="flex items-start justify-between gap-2">
        <h3 className="text-[12px] font-medium leading-snug text-foreground">{article.title}</h3>
        {article.starred && <Star className="size-3.5 text-primary fill-primary shrink-0 mt-0.5" />}
      </div>
      {article.summary && (
        <div className="mt-1.5 flex items-start gap-1.5">
          <Sparkles className="mt-0.5 size-2.5 shrink-0 text-primary/60" />
          <p className="line-clamp-2 text-[11px] leading-relaxed text-muted-foreground/70">
            {article.summary}
          </p>
        </div>
      )}
      <div className="mt-2 flex flex-wrap items-center gap-1.5 font-mono text-[10px]">
        <StatusBadge status={article.status} t={t} />
        <span className="text-muted-foreground/50">
          {t(SOURCE_LABEL_KEYS[article.source_type])}
        </span>
        <span className="text-muted-foreground/50">{formatSavedAt(article.saved_at, t)}</span>
        {article.tags?.map((tag) => (
          <span key={tag} className="rounded-full bg-muted px-1.5 py-0.5 text-muted-foreground/60">
            {tag}
          </span>
        ))}
      </div>
    </button>
  );
}
