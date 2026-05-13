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
      className={`w-full rounded-md border p-3 text-left transition-colors ${
        selected
          ? "border-input bg-accent/70 shadow-sm"
          : "border-border bg-card hover:bg-accent/30"
      }`}
    >
      <div className="flex items-start justify-between gap-2">
        <h3 className="text-sm font-medium leading-snug text-foreground">{article.title}</h3>
        {article.starred && (
          <Star className="size-4 text-amber-500 fill-amber-500 shrink-0 mt-0.5" />
        )}
      </div>
      {article.summary && (
        <div className="mt-1.5 flex items-start gap-1.5">
          <Sparkles className="mt-0.5 size-3 shrink-0 text-primary" />
          <p className="line-clamp-2 text-xs leading-relaxed text-muted-foreground">
            {article.summary}
          </p>
        </div>
      )}
      <div className="mt-2 flex flex-wrap items-center gap-1.5 font-mono text-xs">
        <StatusBadge status={article.status} t={t} />
        <span className="text-muted-foreground">{t(SOURCE_LABEL_KEYS[article.source_type])}</span>
        <span className="text-muted-foreground">{formatSavedAt(article.saved_at, t)}</span>
        {article.tags?.map((tag) => (
          <span key={tag} className="rounded-full bg-muted px-1.5 py-0.5 text-muted-foreground">
            {tag}
          </span>
        ))}
      </div>
    </button>
  );
}
