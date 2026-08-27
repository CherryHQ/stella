import { queryOptions, useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ArrowUpRight, BookOpen } from "lucide-react";
import { getArticle } from "@/lib/api-client";
import type { Article } from "@/lib/api-client/types.gen";
import { useI18n } from "@/lib/i18n";
import type { RenderableReference } from "@/lib/types";
import { StatusBadge } from "@/features/recally/components/StatusBadge";
import { ReferenceCardShell } from "./ReferenceCardShell";

function articleOptions(id: string) {
  return queryOptions({
    queryKey: ["recally", "article", id],
    queryFn: async () => {
      const { data } = await getArticle({ path: { id }, throwOnError: true });
      // SAFETY: getArticle returns the article record under data.
      return data as Article;
    },
    enabled: !!id,
  });
}

export function ArticleReferenceCard({ reference }: { reference: RenderableReference }) {
  const { t } = useI18n();
  const { data: article, isError } = useQuery(articleOptions(reference.id));

  const title = article?.title ?? reference.preview?.title ?? reference.id;

  if (isError) {
    return (
      <ReferenceCardShell
        icon={BookOpen}
        kind={t("references.article")}
        title={t("references.deleted")}
        muted
      />
    );
  }

  return (
    <ReferenceCardShell
      icon={BookOpen}
      kind={t("references.article")}
      title={title}
      status={article ? <StatusBadge status={article.status} t={t} /> : undefined}
      action={
        <Link
          to="/recally"
          search={{ article: reference.id }}
          className="inline-flex shrink-0 items-center gap-1 rounded-lg px-2 py-1 font-mono text-xs text-muted-foreground hover:bg-muted hover:text-foreground"
        >
          {t("references.open")}
          <ArrowUpRight className="size-3.5" />
        </Link>
      }
    />
  );
}
