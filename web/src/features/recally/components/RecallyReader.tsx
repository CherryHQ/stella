import { useState, useEffect, useCallback } from "react";
import { useQuery } from "@tanstack/react-query";
import { MarkdownPreview } from "@/components/MarkdownPreview";
import { Button } from "@/components/ui/button";
import {
  Check,
  EyeOff,
  Archive,
  Trash2,
  Star,
  Sparkles,
  ChevronDown,
  BookOpen,
  Share2,
  Link,
  Loader2,
  MessageCircle,
  Calendar,
} from "lucide-react";
import type { TFunction } from "../constants";
import { getArticleOptions } from "@/lib/api-client/@tanstack/react-query.gen";
import { createShare } from "@/lib/api-client/sdk.gen";
import { formatSavedAt, SOURCE_LABEL_KEYS } from "../constants";
import { StatusBadge } from "./StatusBadge";
import { cn } from "@/lib/utils";

interface ParsedMetadata {
  title?: string;
  url?: string;
  publishedTime?: string;
}

type ArticleUpdateBody = { status: "read" | "unread" | "archived" } | { starred: boolean };

function parseCrawledContent(content: string | null | undefined) {
  if (!content) return { metadata: null, body: "" };

  const titleMatch = content.match(/^Title:\s*(.*?)$/m);
  const urlMatch = content.match(/^URL Source:\s*(.*?)$/m);
  const publishedMatch = content.match(/^Published Time:\s*(.*?)$/m);

  const mdContentIndex = content.indexOf("Markdown Content:");
  let body = content;
  let parsedMetadata: ParsedMetadata | null = null;

  if (mdContentIndex !== -1) {
    const rawBody = content.slice(mdContentIndex + "Markdown Content:".length);
    body = rawBody.replace(/^\s*\n?/, "");

    parsedMetadata = {
      title: titleMatch ? titleMatch[1].trim() : "",
      url: urlMatch ? urlMatch[1].trim() : "",
      publishedTime: publishedMatch ? publishedMatch[1].trim() : "",
    };
  } else if (titleMatch || urlMatch || publishedMatch) {
    let cleanContent = content;
    if (titleMatch) cleanContent = cleanContent.replace(titleMatch[0], "");
    if (urlMatch) cleanContent = cleanContent.replace(urlMatch[0], "");
    if (publishedMatch) cleanContent = cleanContent.replace(publishedMatch[0], "");
    body = cleanContent.trim();

    parsedMetadata = {
      title: titleMatch ? titleMatch[1].trim() : "",
      url: urlMatch ? urlMatch[1].trim() : "",
      publishedTime: publishedMatch ? publishedMatch[1].trim() : "",
    };
  }

  return {
    metadata: parsedMetadata,
    body: body,
  };
}

export function RecallyReader({
  t,
  selectedId,
  chatOpen,
  onToggleChat,
  updateArticleMut,
  deleteArticleMut,
}: {
  t: TFunction;
  selectedId: string | null;
  chatOpen?: boolean;
  onToggleChat?: () => void;
  updateArticleMut: {
    isPending: boolean;
    mutate: (args: { body: ArticleUpdateBody; path: { id: string } }) => void;
  };
  deleteArticleMut: {
    isPending: boolean;
    mutate: (args: { path: { id: string } }) => void;
  };
}) {
  const [summaryExpanded, setSummaryExpanded] = useState(true);
  const [confirmingDeleteId, setConfirmingDeleteId] = useState<string | null>(null);
  const [sharing, setSharing] = useState(false);
  const [shareUrl, setShareUrl] = useState<string | null>(null);
  const [shareError, setShareError] = useState<string | null>(null);

  useEffect(() => {
    setShareUrl(null);
    setShareError(null);
  }, [selectedId]);

  const createArticleShare = useCallback(
    async (articleId: string) => {
      setSharing(true);
      setShareError(null);
      try {
        const { data } = await createShare({
          body: { source: "article", article_id: articleId, expires_in: "7d" },
          throwOnError: true,
        });
        setShareUrl(data.url);
        await navigator.clipboard?.writeText(data.url);
      } catch (e) {
        setShareError(e instanceof Error ? e.message : t("recally.reader.shareFailed"));
      } finally {
        setSharing(false);
      }
    },
    [t],
  );

  const articleQuery = useQuery({
    ...getArticleOptions({
      path: { id: selectedId ?? "" },
      query: { include: "content" },
    }),
    enabled: !!selectedId,
  });

  const selectedArticle = articleQuery.data;
  const parsed = parseCrawledContent(selectedArticle?.content);

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
        <div className="flex h-full items-center justify-center px-8 py-12 text-center bg-background">
          <div className="max-w-md w-full border border-border rounded-xl p-8 bg-card">
            <div className="mx-auto mb-4 flex size-12 items-center justify-center rounded-full bg-primary/10 text-primary">
              <BookOpen className="size-6" />
            </div>
            <h3 className="text-base font-semibold text-foreground">{t("recally.reader.empty")}</h3>
            <p className="text-xs text-muted-foreground mt-1 max-w-72 mx-auto leading-relaxed">
              {t("recally.reader.emptyDesc")}
            </p>
          </div>
        </div>
      ) : articleQuery.isLoading ? (
        <div className="flex h-full items-center justify-center text-xs font-mono text-muted-foreground">
          <Loader2 className="size-4 animate-spin text-primary/75 mr-2" />
          <span>{t("common.loading")}</span>
        </div>
      ) : articleQuery.isError ? (
        <div className="flex h-full items-center justify-center text-xs font-mono text-destructive-foreground">
          {t("common.error")}
        </div>
      ) : selectedArticle ? (
        <div className="mx-auto max-w-3xl px-6 py-6 md:px-8 md:py-8">
          {/* Header Toolbar */}
          <div className="mb-6 flex flex-wrap items-center justify-between gap-3 border-b border-border pb-4">
            <div className="flex flex-wrap gap-1.5 text-xs font-mono text-muted-foreground">
              <StatusBadge status={selectedArticle.status} t={t} />
              <span className="rounded bg-muted/40 border border-border px-2 py-0.5">
                {t(SOURCE_LABEL_KEYS[selectedArticle.source_type])}
              </span>
              <span className="rounded bg-muted/40 border border-border px-2 py-0.5">
                {formatSavedAt(selectedArticle.saved_at, t)}
              </span>
            </div>

            <div className="flex flex-wrap items-center gap-1.5">
              {/* Chat Toggle */}
              {onToggleChat && (
                <button
                  onClick={onToggleChat}
                  className={cn(
                    "inline-flex items-center justify-center p-2 rounded-lg border transition-colors duration-120 cursor-pointer gap-1.5 text-xs font-medium",
                    chatOpen
                      ? "bg-primary/10 text-primary border-primary/25"
                      : "bg-card border-border text-muted-foreground hover:text-foreground hover:bg-muted",
                  )}
                  title={t("recally.reader.discussWithAI")}
                >
                  <MessageCircle className="size-4" />
                  <span className="hidden md:inline">{t("recally.reader.discuss")}</span>
                </button>
              )}

              {/* Status Update Controls */}
              {selectedArticle.status !== "read" ? (
                <Button
                  variant="outline"
                  size="icon-sm"
                  onClick={() =>
                    updateArticleMut.mutate({
                      body: { status: "read" },
                      path: { id: selectedArticle.id },
                    })
                  }
                  disabled={updateArticleMut.isPending}
                  title={t("recally.action.markRead")}
                >
                  <Check className="size-4" />
                </Button>
              ) : (
                <Button
                  variant="outline"
                  size="icon-sm"
                  onClick={() =>
                    updateArticleMut.mutate({
                      body: { status: "unread" },
                      path: { id: selectedArticle.id },
                    })
                  }
                  disabled={updateArticleMut.isPending}
                  title={t("recally.action.markUnread")}
                >
                  <EyeOff className="size-4" />
                </Button>
              )}

              {selectedArticle.status !== "archived" && (
                <Button
                  variant="outline"
                  size="icon-sm"
                  onClick={() =>
                    updateArticleMut.mutate({
                      body: { status: "archived" },
                      path: { id: selectedArticle.id },
                    })
                  }
                  disabled={updateArticleMut.isPending}
                  title={t("recally.action.archive")}
                >
                  <Archive className="size-4" />
                </Button>
              )}

              {/* Star */}
              <Button
                variant="outline"
                size="icon-sm"
                onClick={() =>
                  updateArticleMut.mutate({
                    body: { starred: !selectedArticle.starred },
                    path: { id: selectedArticle.id },
                  })
                }
                disabled={updateArticleMut.isPending}
                title={
                  selectedArticle.starred ? t("recally.action.unstar") : t("recally.action.star")
                }
              >
                <Star
                  className={cn(
                    "size-4",
                    selectedArticle.starred ? "fill-warning text-warning" : "",
                  )}
                />
              </Button>

              {/* Share / Copy Link */}
              {shareUrl ? (
                <Button
                  variant="outline"
                  size="icon-sm"
                  onClick={() => navigator.clipboard?.writeText(shareUrl)}
                  className="border-primary/20 bg-primary/10 text-primary hover:bg-primary/20"
                  title={t("recally.reader.copyShareLink")}
                >
                  <Link className="size-4" />
                </Button>
              ) : (
                <Button
                  variant="outline"
                  size="icon-sm"
                  onClick={() => createArticleShare(selectedArticle.id)}
                  loading={sharing}
                  title={t("recally.action.share")}
                >
                  <Share2 className="size-4" />
                </Button>
              )}

              {shareError && (
                <span className="text-xs text-destructive-foreground">{shareError}</span>
              )}

              {/* Delete / Destructive actions */}
              {confirmingDeleteId === selectedArticle.id ? (
                <div className="flex items-center gap-1 bg-destructive/5 border border-destructive/20 rounded-lg p-1">
                  <span className="text-xs font-medium text-destructive-foreground px-1.5">
                    {t("common.confirm")}
                  </span>
                  <button
                    onClick={() =>
                      deleteArticleMut.mutate({
                        path: { id: selectedArticle.id },
                      })
                    }
                    disabled={deleteArticleMut.isPending}
                    className="rounded bg-destructive/10 border border-destructive/20 px-2 py-0.5 text-xs text-destructive-foreground transition-colors hover:bg-destructive/20 disabled:opacity-50 cursor-pointer"
                  >
                    {t("common.yes")}
                  </button>
                  <button
                    onClick={() => setConfirmingDeleteId(null)}
                    className="rounded border border-border bg-card px-2 py-0.5 text-xs transition-colors hover:bg-accent cursor-pointer"
                  >
                    {t("common.no")}
                  </button>
                </div>
              ) : (
                <Button
                  variant="destructive-outline"
                  size="icon-sm"
                  onClick={() => setConfirmingDeleteId(selectedArticle.id)}
                  disabled={deleteArticleMut.isPending}
                  title={t("common.delete")}
                >
                  <Trash2 className="size-4" />
                </Button>
              )}
            </div>
          </div>

          <article className="w-full">
            <h2 className="mb-2 text-2xl font-semibold leading-tight tracking-tight text-foreground font-sans">
              {selectedArticle.title}
            </h2>
            {selectedArticle.author && (
              <p className="mb-4 font-mono text-xs text-muted-foreground">
                {selectedArticle.author}
              </p>
            )}

            {/* Gorgeous Metadata Card */}
            {parsed.metadata && (parsed.metadata.url || parsed.metadata.publishedTime) && (
              <div className="mb-5 rounded-xl border border-border bg-card p-4.5 text-xs text-muted-foreground font-mono space-y-3.5">
                {parsed.metadata.url && (
                  <div className="flex items-start gap-2.5">
                    <Link className="size-4 text-primary/75 shrink-0 mt-0.5" />
                    <div className="min-w-0 flex-1">
                      <span className="text-xs font-semibold text-muted-foreground block leading-none mb-1">
                        {t("recally.reader.sourceUrl")}
                      </span>
                      <a
                        href={parsed.metadata.url}
                        target="_blank"
                        rel="noopener noreferrer"
                        className="truncate text-xs text-foreground hover:text-primary transition-colors hover:underline block leading-tight"
                      >
                        {parsed.metadata.url}
                      </a>
                    </div>
                  </div>
                )}
                {parsed.metadata.publishedTime && (
                  <div className="flex items-start gap-2.5">
                    <Calendar className="size-4 text-primary/75 shrink-0 mt-0.5" />
                    <div className="min-w-0 flex-1">
                      <span className="text-xs font-semibold text-muted-foreground block leading-none mb-1">
                        {t("recally.reader.publishedTime")}
                      </span>
                      <span className="text-xs text-foreground block leading-tight">
                        {parsed.metadata.publishedTime}
                      </span>
                    </div>
                  </div>
                )}
              </div>
            )}

            {/* AI Summary card */}
            {selectedArticle.summary && (
              <div className="mb-6 rounded-xl border border-primary/20 bg-primary/5 p-4.5">
                <button
                  type="button"
                  onClick={() => setSummaryExpanded(!summaryExpanded)}
                  className="flex w-full items-center gap-1.5 focus:outline-none"
                >
                  <Sparkles className="size-4 text-primary" />
                  <span className="text-xs font-semibold text-primary">
                    {t("recally.summary.label")}
                  </span>
                  <ChevronDown
                    className={cn(
                      "ml-auto size-3.5 text-primary/75 transition-transform duration-120",
                      summaryExpanded && "rotate-180",
                    )}
                  />
                </button>
                {summaryExpanded && (
                  <MarkdownPreview
                    content={selectedArticle.summary}
                    className="mt-2 text-foreground"
                  />
                )}
              </div>
            )}

            {/* Content Body */}
            {parsed.body ? (
              <MarkdownPreview
                content={parsed.body}
                className="text-foreground leading-relaxed font-sans text-base"
              />
            ) : (
              <div className="rounded-xl border border-border bg-card p-6 text-center space-y-3">
                <div className="mx-auto flex size-10 items-center justify-center rounded-full bg-muted/40 text-muted-foreground">
                  <BookOpen className="size-5" />
                </div>
                <div>
                  <h4 className="text-sm font-semibold text-foreground">
                    {t("recally.reader.noContentParsed")}
                  </h4>
                  <p className="text-xs text-muted-foreground max-w-96 mx-auto mt-1 leading-normal">
                    {t("recally.reader.noContentDesc")}
                  </p>
                </div>
              </div>
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
