import { useEffect, useState } from "react";
import { ExternalLink } from "lucide-react";
import { getWorkspaceFileContent } from "@/lib/api-client/sdk.gen";
import { isNonTextFile } from "@/lib/file-kind";
import { useI18n } from "@/lib/i18n";
import { MarkdownPreview } from "@/components/MarkdownPreview";
import { Button } from "@/components/ui/button";
import { Dialog, DialogHeader, DialogPopup, DialogTitle } from "@/components/ui/dialog";
import { attachmentDisplayName, workspaceFileURL } from "./utils";

interface Props {
  agentId: string;
  sessionId: string;
  /** Workspace path of the attachment, or null when the dialog is closed. */
  path: string | null;
  onClose: () => void;
}

/**
 * Read-only preview for a message attachment. Text files are read through the
 * workspace API and rendered in place; anything binary keeps the raw URL as
 * its only affordance, since the browser previews those better than we can.
 */
export function AttachmentPreview({ agentId, sessionId, path, onClose }: Props) {
  const { t } = useI18n();
  const [content, setContent] = useState<string | null>(null);
  const [language, setLanguage] = useState("");
  const [error, setError] = useState(false);

  useEffect(() => {
    setContent(null);
    setError(false);
    setLanguage("");
    if (!path || isNonTextFile(path)) return;

    let cancelled = false;
    void (async () => {
      try {
        const { data } = await getWorkspaceFileContent({
          path: { agentId, sessionId },
          query: { path },
          throwOnError: true,
        });
        if (cancelled) return;
        const file = data as { content?: string; language?: string };
        setContent(file.content ?? "");
        setLanguage(file.language ?? "");
      } catch (e) {
        console.error("[attachment preview]", e);
        if (!cancelled) setError(true);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [agentId, sessionId, path]);

  if (!path) return null;
  const rawURL = workspaceFileURL(agentId, sessionId, path);
  const previewable = !isNonTextFile(path);

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogPopup className="flex max-h-[85vh] w-[90vw] max-w-4xl flex-col overflow-hidden p-0">
        {/* pr-10 keeps the actions clear of the dialog's own close button. */}
        <DialogHeader className="flex-row items-center justify-between gap-2 border-b border-border py-3 pl-4 pr-10">
          <DialogTitle className="min-w-0 truncate font-mono text-sm">
            {attachmentDisplayName(path)}
          </DialogTitle>
          <Button variant="ghost" size="xs" render={<a href={rawURL} target="_blank" />}>
            <ExternalLink className="size-3.5" />
            {t("sessions.attachment.openRaw")}
          </Button>
        </DialogHeader>

        <div className="min-h-0 flex-1 overflow-y-auto">
          {!previewable && (
            <p className="px-4 py-8 text-center text-sm text-muted-foreground">
              {t("sessions.attachment.notPreviewable")}
            </p>
          )}
          {previewable && error && (
            <p className="px-4 py-8 text-center text-sm text-destructive-foreground">
              {t("sessions.attachment.loadFailed")}
            </p>
          )}
          {previewable && !error && content === null && (
            <div className="flex justify-center py-8">
              <div className="size-5 animate-spin rounded-full border-2 border-muted border-t-muted-foreground" />
            </div>
          )}
          {previewable && !error && content !== null && language === "markdown" && (
            <MarkdownPreview content={content} className="px-6 py-4" />
          )}
          {previewable && !error && content !== null && language !== "markdown" && (
            <pre className="overflow-x-auto px-4 py-3 font-mono text-xs leading-relaxed text-foreground">
              {content}
            </pre>
          )}
        </div>
      </DialogPopup>
    </Dialog>
  );
}
