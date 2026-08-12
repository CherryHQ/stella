import { useEffect, useRef } from "react";
import { MarkdownPreview } from "@/components/MarkdownPreview";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { useI18n } from "@/lib/i18n";

interface FileEditorState {
  open: boolean;
  path: string;
  content: string;
  language: string;
  saving: boolean;
  loading: boolean;
  previewMd: boolean;
}

interface Props {
  fileEditor: FileEditorState;
  onClose: () => void;
  onSave: () => Promise<void>;
  onChange: (content: string) => void;
  onTogglePreview: () => void;
}

export function FileEditorModal({ fileEditor, onClose, onSave, onChange, onTogglePreview }: Props) {
  const { t } = useI18n();
  const textareaRef = useRef<HTMLTextAreaElement>(null);

  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", handler);
    return () => window.removeEventListener("keydown", handler);
  }, [onClose]);

  useEffect(() => {
    if (!fileEditor.loading && !fileEditor.previewMd) {
      textareaRef.current?.focus();
    }
  }, [fileEditor.loading, fileEditor.previewMd]);

  return (
    <div
      className="fixed inset-0 flex items-center justify-center bg-foreground/50 z-[10000]"
      onKeyDown={(e) => e.key === "Escape" && onClose()}
    >
      <div
        className="bg-background rounded-xl shadow-2xl border border-border flex flex-col overflow-hidden"
        style={{ width: "90vw", height: "85vh", maxWidth: "72rem" }}
      >
        {/* Header */}
        <div className="flex items-center justify-between px-4 py-2.5 border-b border-border flex-shrink-0">
          <div className="flex items-center gap-2 min-w-0">
            <svg
              className="w-3.5 h-3.5 shrink-0 text-muted-foreground"
              fill="none"
              viewBox="0 0 24 24"
              strokeWidth="1.8"
              stroke="currentColor"
            >
              <path
                strokeLinecap="round"
                strokeLinejoin="round"
                d="M19.5 14.25v-2.625a3.375 3.375 0 0 0-3.375-3.375h-1.5A1.125 1.125 0 0 1 13.5 7.125v-1.5a3.375 3.375 0 0 0-3.375-3.375H8.25m0 12.75h7.5m-7.5 3H12M10.5 2.25H5.625c-.621 0-1.125.504-1.125 1.125v17.25c0 .621.504 1.125 1.125 1.125h12.75c.621 0 1.125-.504 1.125-1.125V11.25a9 9 0 0 0-9-9Z"
              />
            </svg>
            <span className="text-xs font-mono text-muted-foreground truncate">
              {fileEditor.path}
            </span>
            {fileEditor.language && (
              <span className="text-xs border border-border rounded-full px-1.5 py-0.5 font-mono">
                {fileEditor.language}
              </span>
            )}
          </div>
          <div className="flex items-center gap-1 shrink-0">
            {fileEditor.language === "markdown" && (
              <Button
                variant="ghost"
                size="xs"
                onClick={onTogglePreview}
                className={cn(
                  "text-xs",
                  fileEditor.previewMd ? "text-primary" : "text-muted-foreground",
                )}
              >
                Preview
              </Button>
            )}
            <span className="text-xs font-mono text-muted-foreground select-none hidden sm:block">
              ⌘S
            </span>
            <Button
              size="xs"
              disabled={fileEditor.saving || fileEditor.loading}
              onClick={() => onSave().catch(console.error)}
              className="gap-1"
            >
              {fileEditor.saving && (
                <div className="w-3 h-3 border border-current/30 border-t-current rounded-full animate-spin" />
              )}
              {t("common.save")}
            </Button>
            <Button
              variant="ghost"
              size="xs"
              onClick={onClose}
              className="px-2 text-muted-foreground hover:text-foreground"
            >
              ✕
            </Button>
          </div>
        </div>

        {/* Editor / Preview */}
        <div className="flex-1 overflow-hidden relative">
          {fileEditor.loading && (
            <div className="absolute inset-0 flex items-center justify-center bg-background/80 z-10">
              <div className="w-5 h-5 border-2 border-muted border-t-muted-foreground rounded-full animate-spin" />
            </div>
          )}
          {fileEditor.previewMd && !fileEditor.loading && (
            <MarkdownPreview
              content={fileEditor.content}
              className="absolute inset-0 overflow-y-auto px-8 py-6"
            />
          )}
          {!fileEditor.previewMd && !fileEditor.loading && (
            <textarea
              ref={textareaRef}
              value={fileEditor.content}
              onChange={(e) => onChange(e.target.value)}
              onKeyDown={(e) => {
                if ((e.metaKey || e.ctrlKey) && e.key === "s") {
                  e.preventDefault();
                  onSave().catch(console.error);
                }
              }}
              className="absolute inset-0 w-full h-full resize-none border-0 outline-none bg-background text-foreground p-4"
              style={{
                fontFamily: "'JetBrains Mono', monospace",
                fontSize: 13,
                lineHeight: 1.6,
                tabSize: 2,
              }}
              spellCheck={false}
              autoComplete="off"
              autoCorrect="off"
            />
          )}
        </div>
      </div>
    </div>
  );
}
