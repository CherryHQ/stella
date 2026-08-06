import { Star } from "lucide-react";
import type { Article } from "@/lib/api-client/types.gen";
import type { TFunction } from "../constants";
import { formatSavedAt } from "../constants";
import { cn } from "@/lib/utils";

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
      className={cn(
        "w-full rounded-lg border px-3 py-2.5 text-left transition-colors duration-120 cursor-pointer",
        selected
          ? "border-primary/30 bg-primary/5 text-foreground ring-1 ring-primary/20"
          : "border-transparent hover:bg-muted",
        article.status === "unread" && !selected && "border-l-2 border-l-primary/50",
      )}
    >
      <div className="flex items-start justify-between gap-2">
        <h3 className="text-[12px] font-semibold leading-snug text-foreground line-clamp-2">
          {article.title}
        </h3>
        {article.starred && <Star className="size-3 text-warning fill-warning shrink-0 mt-0.5" />}
      </div>
      {article.summary && (
        <p className="mt-1 line-clamp-2 text-xs leading-relaxed text-muted-foreground">
          {article.summary}
        </p>
      )}
      <div className="mt-1.5 text-xs font-mono text-muted-foreground">
        {formatSavedAt(article.saved_at, t)}
      </div>
    </button>
  );
}
