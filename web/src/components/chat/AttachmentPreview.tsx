import { useEffect, useState } from "react";
import { ExternalLink } from "lucide-react";
import { getWorkspaceFileContent } from "@/lib/api-client/sdk.gen";
import { extOf, fetchBlobUrl, isBinary, isPdf, mimeTypeForPath } from "@/lib/file-kind";
import { highlightToHtml } from "@/lib/highlight";
import { useI18n } from "@/lib/i18n";
import { cn } from "@/lib/utils";
import { useMediaQuery } from "@/hooks/use-media-query";
import { MarkdownPreview } from "@/components/MarkdownPreview";
import { Button } from "@/components/ui/button";
import { Dialog, DialogHeader, DialogPopup, DialogTitle } from "@/components/ui/dialog";
import { attachmentDisplayName, workspaceFileURL, workspaceScopeFor } from "./utils";

interface Props {
  agentId: string;
  sessionId: string;
  /** Workspace path of the attachment, or null when the dialog is closed. */
  path: string | null;
  onClose: () => void;
}

/**
 * Read-only preview for a message attachment. Text renders in place (markdown
 * formatted, source highlighted), PDFs render in the browser's own viewer, and
 * archives keep the raw URL as their only affordance.
 */
export function AttachmentPreview({ agentId, sessionId, path, onClose }: Props) {
  const { t } = useI18n();
  const [content, setContent] = useState<string | null>(null);
  const [language, setLanguage] = useState("");
  const [highlighted, setHighlighted] = useState<string | null>(null);
  const [pdfUrl, setPdfUrl] = useState<string | null>(null);
  const [error, setError] = useState(false);

  // Mobile browsers do not render a PDF inside an iframe (iOS Safari shows the
  // first page at best, Chrome for Android shows nothing), so there the file
  // gets handed to the platform viewer through "Open raw" instead.
  const isTouch = useMediaQuery({ pointer: "coarse" });
  const rawURL = path ? workspaceFileURL(agentId, sessionId, path) : "";
  const kind = !path
    ? "none"
    : isPdf(path)
      ? isTouch
        ? "external"
        : "pdf"
      : isBinary(path)
        ? "binary"
        : "text";

  // The raw endpoint may serve a PDF as a download; a blob URL with an explicit
  // media type keeps it inline in the iframe.
  useEffect(() => {
    setPdfUrl(null);
    if (kind !== "pdf") return;
    let url: string | null = null;
    let cancelled = false;
    fetchBlobUrl(rawURL, mimeTypeForPath(path ?? ""))
      .then((u) => {
        if (cancelled) {
          URL.revokeObjectURL(u);
          return;
        }
        url = u;
        setPdfUrl(u);
      })
      .catch(() => {
        if (!cancelled) setError(true);
      });
    return () => {
      cancelled = true;
      if (url) URL.revokeObjectURL(url);
    };
  }, [kind, rawURL, path]);

  useEffect(() => {
    setContent(null);
    setLanguage("");
    setHighlighted(null);
    setError(false);
    if (!path || kind !== "text") return;

    let cancelled = false;
    void (async () => {
      try {
        const { data } = await getWorkspaceFileContent({
          path: { agentId, sessionId },
          query: { path, scope: workspaceScopeFor(path) },
          throwOnError: true,
        });
        if (cancelled) return;
        const file = data as { content?: string; language?: string };
        const text = file.content ?? "";
        const lang = file.language ?? "";
        setContent(text);
        setLanguage(lang);
        if (isMarkdown(lang)) return;
        const html = await highlightToHtml(text, lang || extOf(path));
        if (!cancelled && html) setHighlighted(html);
      } catch (e) {
        console.error("[attachment preview]", e);
        if (!cancelled) setError(true);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [agentId, sessionId, path, kind]);

  if (!path) return null;
  const name = attachmentDisplayName(path);

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      {/* The PDF iframe needs a height to fill; everything else shrinks to fit. */}
      <DialogPopup
        className={cn(
          // Width comes from the popup's own w-full so the mobile bottom sheet
          // still spans the screen; only the desktop ceiling is ours.
          "flex max-w-4xl flex-col overflow-hidden p-0 sm:w-[90vw]",
          kind === "pdf" ? "h-[85vh]" : "max-h-[85vh]",
        )}
      >
        {/* pr-10 keeps the actions clear of the dialog's own close button. */}
        <DialogHeader className="flex-row items-center justify-between gap-2 border-b border-border py-3 pl-4 pr-10">
          <DialogTitle className="min-w-0 truncate font-mono text-sm">{name}</DialogTitle>
          <Button variant="ghost" size="xs" render={<a href={pdfUrl ?? rawURL} target="_blank" />}>
            <ExternalLink className="size-3.5" />
            {t("sessions.attachment.openRaw")}
          </Button>
        </DialogHeader>

        <div className="min-h-0 flex-1 overflow-y-auto">
          {error ? (
            <Notice tone="error">{t("sessions.attachment.loadFailed")}</Notice>
          ) : kind === "binary" ? (
            <Notice>{t("sessions.attachment.notPreviewable")}</Notice>
          ) : kind === "external" ? (
            <Notice>{t("sessions.attachment.openToView")}</Notice>
          ) : kind === "pdf" ? (
            pdfUrl ? (
              <iframe src={pdfUrl} title={name} className="size-full border-0" />
            ) : (
              <Spinner />
            )
          ) : content === null ? (
            <Spinner />
          ) : highlighted ? (
            <div
              className="text-xs leading-relaxed [&_.shiki]:!bg-transparent [&_code]:!bg-transparent [&_pre]:m-0 [&_pre]:!bg-transparent [&_pre]:px-4 [&_pre]:py-3"
              dangerouslySetInnerHTML={{ __html: highlighted }}
            />
          ) : isMarkdown(language) ? (
            <MarkdownPreview content={content} className="px-6 py-4" />
          ) : (
            <pre className="overflow-x-auto px-4 py-3 font-mono text-xs leading-relaxed text-foreground">
              {content}
            </pre>
          )}
        </div>
      </DialogPopup>
    </Dialog>
  );
}

function isMarkdown(language: string): boolean {
  return language === "markdown" || language === "md";
}

function Notice({ children, tone }: { children: React.ReactNode; tone?: "error" }) {
  return (
    <p
      className={
        tone === "error"
          ? "px-4 py-8 text-center text-sm text-destructive-foreground"
          : "px-4 py-8 text-center text-sm text-muted-foreground"
      }
    >
      {children}
    </p>
  );
}

function Spinner() {
  return (
    <div className="flex justify-center py-8">
      <div className="size-5 animate-spin rounded-full border-2 border-muted border-t-muted-foreground" />
    </div>
  );
}
