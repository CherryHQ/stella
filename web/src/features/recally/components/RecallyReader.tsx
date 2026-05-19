import { useState, useEffect } from "react";
import { useQuery } from "@tanstack/react-query";
import { Streamdown } from "streamdown";
import {
  Check,
  EyeOff,
  Archive,
  Trash2,
  Star,
  Sparkles,
  ChevronDown,
  BookOpen,
} from "lucide-react";
import type { TFunction } from "../constants";
import { getArticleOptions } from "@/lib/api-client/@tanstack/react-query.gen";
import { formatSavedAt, SOURCE_LABEL_KEYS } from "../constants";
import { StatusBadge } from "./StatusBadge";

export function RecallyReader({
  t,
  selectedId,
  updateArticleMut,
  deleteArticleMut,
}: {
  t: TFunction;
  selectedId: string | null;
  updateArticleMut: {
    isPending: boolean;
    mutate: (args: { body: Record<string, unknown>; path: { id: string } }) => void;
  };
  deleteArticleMut: {
    isPending: boolean;
    mutate: (args: { path: { id: string } }) => void;
  };
}) {
  const [summaryExpanded, setSummaryExpanded] = useState(true);
  const [confirmingDeleteId, setConfirmingDeleteId] = useState<string | null>(null);

  const articleQuery = useQuery({
    ...getArticleOptions({
      path: { id: selectedId ?? "" },
      query: { include: "content" },
    }),
    enabled: !!selectedId,
  });

  const selectedArticle = articleQuery.data;

  // Auto-mark unread articles as read when opened
  useEffect(() => {
    if (selectedArticle?.status === "unread" && !updateArticleMut.isPending) {
      updateArticleMut.mutate({
        body: { status: "read" },
        path: { id: selectedArticle.id },
      });
    }
  }, [selectedArticle?.id, selectedArticle?.status, updateArticleMut.isPending]);

  return (
    <div className="min-h-0 flex-1 overflow-auto bg-background">
      {!selectedId ? (
        <div className="flex h-full items-center justify-center px-8 text-center">
          <div className="max-w-72">
            <div className="mx-auto mb-3 flex size-10 items-center justify-center rounded-md border border-border bg-card text-muted-foreground">
              <BookOpen className="size-5" />
            </div>
            <p className="text-sm font-medium text-foreground">{t("recally.reader.empty")}</p>
          </div>
        </div>
      ) : articleQuery.isLoading ? (
        <div className="flex h-full items-center justify-center text-sm text-muted-foreground">
          {t("common.loading")}
        </div>
      ) : articleQuery.isError ? (
        <div className="flex h-full items-center justify-center text-sm text-destructive">
          {t("common.error")}
        </div>
      ) : selectedArticle ? (
        <div className="mx-auto max-w-[760px] px-6 py-6">
          <div className="mb-5 flex flex-wrap items-center justify-between gap-3 border-b border-border pb-4">
            <div className="flex flex-wrap gap-1.5 text-xs font-mono text-muted-foreground">
              <StatusBadge status={selectedArticle.status} t={t} />
              <span className="rounded-full border border-border bg-card px-2 py-0.5">
                {t(SOURCE_LABEL_KEYS[selectedArticle.source_type])}
              </span>
              <span className="rounded-full border border-border bg-card px-2 py-0.5">
                {formatSavedAt(selectedArticle.saved_at, t)}
              </span>
            </div>
            <div className="flex flex-wrap items-center gap-1">
              {selectedArticle.status !== "read" && (
                <button
                  onClick={() =>
                    updateArticleMut.mutate({
                      body: { status: "read" },
                      path: { id: selectedArticle.id },
                    })
                  }
                  disabled={updateArticleMut.isPending}
                  className="inline-flex items-center gap-1 rounded-md border border-border bg-card px-2 py-1 text-xs transition-colors hover:bg-accent disabled:opacity-50"
                >
                  <Check className="size-3" />
                  {t("recally.action.markRead")}
                </button>
              )}
              {selectedArticle.status !== "unread" && (
                <button
                  onClick={() =>
                    updateArticleMut.mutate({
                      body: { status: "unread" },
                      path: { id: selectedArticle.id },
                    })
                  }
                  disabled={updateArticleMut.isPending}
                  className="inline-flex items-center gap-1 rounded-md border border-border bg-card px-2 py-1 text-xs transition-colors hover:bg-accent disabled:opacity-50"
                >
                  <EyeOff className="size-3" />
                  {t("recally.action.markUnread")}
                </button>
              )}
              {selectedArticle.status !== "archived" && (
                <button
                  onClick={() =>
                    updateArticleMut.mutate({
                      body: { status: "archived" },
                      path: { id: selectedArticle.id },
                    })
                  }
                  disabled={updateArticleMut.isPending}
                  className="inline-flex items-center gap-1 rounded-md border border-border bg-card px-2 py-1 text-xs transition-colors hover:bg-accent disabled:opacity-50"
                >
                  <Archive className="size-3" />
                  {t("recally.action.archive")}
                </button>
              )}
              <button
                onClick={() =>
                  updateArticleMut.mutate({
                    body: { starred: !selectedArticle.starred },
                    path: { id: selectedArticle.id },
                  })
                }
                disabled={updateArticleMut.isPending}
                className="inline-flex items-center gap-1 rounded-md border border-border bg-card px-2 py-1 text-xs transition-colors hover:bg-accent disabled:opacity-50"
              >
                <Star
                  className={`size-3 ${selectedArticle.starred ? "fill-primary text-primary" : ""}`}
                />
                {selectedArticle.starred ? t("recally.action.unstar") : t("recally.action.star")}
              </button>
              {confirmingDeleteId === selectedArticle.id ? (
                <div className="flex items-center gap-1">
                  <span className="text-xs font-medium text-destructive">
                    {t("recally.deleteConfirm")}
                  </span>
                  <button
                    onClick={() =>
                      deleteArticleMut.mutate({
                        path: { id: selectedArticle.id },
                      })
                    }
                    disabled={deleteArticleMut.isPending}
                    className="rounded-md border border-destructive/30 bg-destructive/10 px-2 py-1 text-xs text-destructive transition-colors hover:bg-destructive/20 disabled:opacity-50"
                  >
                    {t("common.yes")}
                  </button>
                  <button
                    onClick={() => setConfirmingDeleteId(null)}
                    className="rounded-md border border-border bg-card px-2 py-1 text-xs transition-colors hover:bg-accent"
                  >
                    {t("common.no")}
                  </button>
                </div>
              ) : (
                <button
                  onClick={() => setConfirmingDeleteId(selectedArticle.id)}
                  disabled={deleteArticleMut.isPending}
                  className="inline-flex items-center gap-1 rounded-md border border-destructive/30 px-2 py-1 text-xs text-destructive transition-colors hover:bg-destructive/10 disabled:opacity-50"
                >
                  <Trash2 className="size-3" />
                  {t("common.delete")}
                </button>
              )}
            </div>
          </div>
          <article className="w-full">
            <h2 className="mb-2 text-2xl font-semibold leading-tight tracking-tight text-foreground">
              {selectedArticle.title}
            </h2>
            {selectedArticle.author && (
              <p className="mb-3 font-mono text-xs text-muted-foreground">
                {selectedArticle.author}
              </p>
            )}
            {selectedArticle.summary && (
              <div className="mb-4 rounded-md border border-border bg-accent/5 p-3">
                <button
                  type="button"
                  onClick={() => setSummaryExpanded(!summaryExpanded)}
                  className="flex w-full items-center gap-1.5"
                >
                  <Sparkles className="size-3.5 text-primary" />
                  <span className="text-[11px] font-semibold uppercase tracking-wider text-primary">
                    {t("recally.summary.label")}
                  </span>
                  <ChevronDown
                    className={`ml-auto size-3 text-muted-foreground transition-transform ${summaryExpanded ? "rotate-180" : ""}`}
                  />
                </button>
                {summaryExpanded && (
                  <div className="prose prose-sm mt-1.5 max-w-none text-foreground prose-headings:text-foreground prose-a:text-primary">
                    <Streamdown>{selectedArticle.summary}</Streamdown>
                  </div>
                )}
              </div>
            )}
            {selectedArticle.content ? (
              <div className="prose prose-sm max-w-none text-foreground prose-headings:text-foreground prose-a:text-primary">
                <Streamdown>{selectedArticle.content}</Streamdown>
              </div>
            ) : (
              <p className="text-sm italic text-muted-foreground">
                {t("recally.reader.noContent")}
              </p>
            )}
          </article>
        </div>
      ) : (
        <div className="flex h-full items-center justify-center text-sm text-muted-foreground">
          {t("recally.reader.empty")}
        </div>
      )}
    </div>
  );
}
