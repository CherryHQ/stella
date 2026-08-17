import { useCallback, useEffect, useRef, useState } from "react";
import { MarkdownPreview } from "@/components/MarkdownPreview";
import type { Highlighter } from "shiki";
import {
  ArrowLeft,
  Pencil,
  Save,
  X,
  Loader2,
  ExternalLink,
  Download,
  Eye,
  Code2,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { useI18n } from "@/lib/i18n";
import {
  extOf,
  isImage,
  isPdf,
  isBinary,
  isHtml,
  fetchBlobUrl,
  mimeTypeForPath,
} from "@/lib/file-kind";

function isMarkdown(lang: string): boolean {
  return lang === "markdown" || lang === "md";
}

let highlighterPromise: Promise<Highlighter> | null = null;

function getHighlighter(): Promise<Highlighter> {
  if (!highlighterPromise) {
    // Dynamic import keeps shiki (and its grammars) out of the main chunk;
    // it only loads once someone actually opens a file.
    highlighterPromise = import("shiki").then(({ createHighlighter }) =>
      createHighlighter({
        themes: ["github-light", "github-dark"],
        langs: [],
      }),
    );
  }
  return highlighterPromise;
}

function shikiLang(language: string): string {
  const map: Record<string, string> = {
    js: "javascript",
    ts: "typescript",
    jsx: "jsx",
    tsx: "tsx",
    py: "python",
    rb: "ruby",
    rs: "rust",
    yml: "yaml",
    md: "markdown",
    sh: "bash",
    zsh: "bash",
    dockerfile: "dockerfile",
    makefile: "makefile",
  };
  return map[language.toLowerCase()] ?? language.toLowerCase();
}

interface Props {
  path: string;
  content: string;
  language: string;
  loading: boolean;
  saving: boolean;
  agentID: string;
  sessionID: string;
  scope?: "agent" | "user";
  onBack: () => void;
  onSave: (content: string) => Promise<void>;
}

export function FileViewer({
  path,
  content,
  language,
  loading,
  saving,
  agentID,
  sessionID,
  scope,
  onBack,
  onSave,
}: Props) {
  const { t } = useI18n();
  const [editing, setEditing] = useState(false);
  const [previewMode, setPreviewMode] = useState(false);
  const [editContent, setEditContent] = useState("");
  const [highlighted, setHighlighted] = useState<string>("");
  const [highlightReady, setHighlightReady] = useState(false);
  const [viewBlobUrl, setViewBlobUrl] = useState<string | null>(null);
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const ext = extOf(path);
  const fileName = path.split("/").pop() ?? path;
  const rawUrl = `/api/agents/${encodeURIComponent(agentID)}/sessions/${encodeURIComponent(sessionID)}/workspace/file-content?path=${encodeURIComponent(path)}&raw=true${scope ? `&scope=${scope}` : ""}`;

  useEffect(() => {
    setEditing(false);
    setPreviewMode(isHtml(path));
    setHighlighted("");
    setHighlightReady(false);
    setViewBlobUrl(null);
  }, [path]);

  useEffect(() => {
    if (loading || isImage(path) || isBinary(path)) return;
    let url: string | null = null;
    let cancelled = false;
    const mime = mimeTypeForPath(path);
    const source = isPdf(path)
      ? fetchBlobUrl(rawUrl, mime)
      : Promise.resolve(URL.createObjectURL(new Blob([content], { type: mime })));
    source
      .then((u) => {
        if (cancelled) {
          URL.revokeObjectURL(u);
          return;
        }
        url = u;
        setViewBlobUrl(u);
      })
      .catch(console.error);
    return () => {
      cancelled = true;
      if (url) URL.revokeObjectURL(url);
    };
  }, [path, rawUrl, content, loading]);

  useEffect(() => {
    if (loading || editing || isImage(path) || isPdf(path) || isBinary(path)) return;
    if (isMarkdown(language)) {
      setHighlightReady(true);
      return;
    }

    let cancelled = false;
    const lang = shikiLang(language || ext);
    if (!lang) {
      setHighlightReady(true);
      return;
    }

    void (async () => {
      try {
        const hl = await getHighlighter();
        const loadedLangs = hl.getLoadedLanguages();
        if (!loadedLangs.includes(lang as never)) {
          try {
            await hl.loadLanguage(lang as never);
          } catch {
            if (!cancelled) {
              setHighlightReady(true);
            }
            return;
          }
        }
        if (cancelled) return;
        const isDark = document.documentElement.classList.contains("dark");
        const html = hl.codeToHtml(content, {
          lang,
          theme: isDark ? "github-dark" : "github-light",
        });
        if (!cancelled) {
          setHighlighted(html);
          setHighlightReady(true);
        }
      } catch {
        if (!cancelled) setHighlightReady(true);
      }
    })();

    return () => {
      cancelled = true;
    };
  }, [content, language, loading, editing, path, ext]);

  const startEditing = useCallback(() => {
    setEditContent(content);
    setEditing(true);
    setPreviewMode(false);
    requestAnimationFrame(() => textareaRef.current?.focus());
  }, [content]);

  const handleSave = useCallback(async () => {
    await onSave(editContent);
    setEditing(false);
  }, [editContent, onSave]);

  const handleKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key === "s") {
        e.preventDefault();
        handleSave().catch(console.error);
      }
      if (e.key === "Escape") {
        setEditing(false);
      }
    },
    [handleSave],
  );

  return (
    <div className="flex flex-col h-full overflow-hidden bg-background">
      {/* Header */}
      <div className="flex items-center gap-1.5 px-2 py-1.5 border-b border-border flex-shrink-0 min-h-[36px]">
        <Button
          variant="ghost"
          size="xs"
          onClick={onBack}
          className="h-6 px-1.5 text-muted-foreground hover:text-foreground"
        >
          <ArrowLeft className="w-3.5 h-3.5" />
        </Button>
        <span className="text-xs font-mono text-foreground truncate flex-1 min-w-0">
          {fileName}
        </span>
        {language && (
          <span className="text-xs font-mono text-muted-foreground shrink-0">{language}</span>
        )}
        {!loading && (
          <a
            href={viewBlobUrl ?? rawUrl}
            target="_blank"
            rel="noopener noreferrer"
            className="p-1 rounded hover:bg-muted transition-colors text-muted-foreground hover:text-foreground"
            title={t("sessions.workspace.openInBrowser")}
          >
            <ExternalLink className="w-3 h-3" />
          </a>
        )}
        {!loading && (
          <a
            href={rawUrl}
            download={fileName}
            className="p-1 rounded hover:bg-muted transition-colors text-muted-foreground hover:text-foreground"
            title={t("sessions.workspace.download")}
          >
            <Download className="w-3 h-3" />
          </a>
        )}
        {!loading && isHtml(path) && !editing && (
          <Button
            variant="ghost"
            size="xs"
            onClick={() => setPreviewMode((m) => !m)}
            className="h-6 px-1.5 text-muted-foreground hover:text-foreground"
            title={previewMode ? "View source" : "Preview"}
          >
            {previewMode ? <Code2 className="w-3 h-3" /> : <Eye className="w-3 h-3" />}
          </Button>
        )}
        {!loading && !isBinary(path) && !isImage(path) && !isPdf(path) && (
          <>
            {editing ? (
              <>
                <Button
                  variant="ghost"
                  size="xs"
                  onClick={() => setEditing(false)}
                  className="h-6 px-1.5 text-muted-foreground"
                >
                  <X className="w-3 h-3" />
                </Button>
                <Button
                  size="xs"
                  onClick={() => handleSave().catch(console.error)}
                  disabled={saving}
                  className="h-6 px-2 gap-1"
                >
                  {saving ? (
                    <Loader2 className="w-3 h-3 animate-spin" />
                  ) : (
                    <Save className="w-3 h-3" />
                  )}
                  {t("common.save")}
                </Button>
              </>
            ) : (
              <Button
                variant="ghost"
                size="xs"
                onClick={startEditing}
                className="h-6 px-1.5 text-muted-foreground hover:text-foreground"
              >
                <Pencil className="w-3 h-3" />
              </Button>
            )}
          </>
        )}
      </div>

      {/* Content */}
      <div className="flex-1 overflow-auto">
        {loading && (
          <div className="flex items-center justify-center h-full">
            <Loader2 className="w-4 h-4 animate-spin text-muted-foreground" />
          </div>
        )}

        {!loading && isImage(path) && (
          <div className="p-4 flex items-center justify-center h-full">
            <img
              src={rawUrl}
              alt={fileName}
              className="max-w-full max-h-full object-contain rounded"
              onError={(e) => {
                (e.target as HTMLImageElement).style.display = "none";
              }}
            />
          </div>
        )}

        {!loading &&
          isPdf(path) &&
          (viewBlobUrl ? (
            <iframe src={viewBlobUrl} title={fileName} className="w-full h-full border-0" />
          ) : (
            <div className="flex items-center justify-center h-full">
              <Loader2 className="w-4 h-4 animate-spin text-muted-foreground" />
            </div>
          ))}

        {!loading &&
          isHtml(path) &&
          previewMode &&
          !editing &&
          (viewBlobUrl ? (
            <iframe
              src={viewBlobUrl}
              title={fileName}
              sandbox="allow-scripts"
              className="w-full h-full border-0 bg-background"
            />
          ) : (
            <div className="flex items-center justify-center h-full">
              <Loader2 className="w-4 h-4 animate-spin text-muted-foreground" />
            </div>
          ))}

        {!loading && !isPdf(path) && isBinary(path) && (
          <div className="flex flex-col items-center justify-center h-full gap-3 text-muted-foreground">
            <span className="text-xs">{t("sessions.workspace.binaryFile")}</span>
            <span className="text-xs font-mono">.{ext}</span>
            <a
              href={rawUrl}
              target="_blank"
              rel="noopener noreferrer"
              className="text-xs font-mono text-primary hover:underline flex items-center gap-1"
            >
              <ExternalLink className="w-3 h-3" />
              Open in browser
            </a>
          </div>
        )}

        {!loading && !isImage(path) && !isBinary(path) && editing && (
          <textarea
            ref={textareaRef}
            value={editContent}
            onChange={(e) => setEditContent(e.target.value)}
            onKeyDown={handleKeyDown}
            className="w-full h-full resize-none border-0 outline-none bg-transparent text-foreground p-3"
            style={{
              fontFamily: "var(--font-mono)",
              fontSize: 12,
              lineHeight: 1.6,
              tabSize: 2,
            }}
            spellCheck={false}
          />
        )}

        {!loading &&
          !isImage(path) &&
          !isBinary(path) &&
          !editing &&
          !(isHtml(path) && previewMode) && (
            <>
              {isMarkdown(language) ? (
                <MarkdownPreview content={content} className="px-4 py-3 [&_*]:text-[12px]" />
              ) : highlighted ? (
                <div
                  className={cn(
                    "text-[12px] leading-relaxed [&_pre]:!bg-transparent [&_pre]:p-3 [&_pre]:m-0 [&_code]:!bg-transparent",
                    "[&_.shiki]:!bg-transparent",
                  )}
                  dangerouslySetInnerHTML={{ __html: highlighted }}
                />
              ) : highlightReady ? (
                <pre className="p-3 text-[12px] leading-relaxed text-foreground whitespace-pre-wrap break-words font-mono">
                  {content}
                </pre>
              ) : (
                <pre className="p-3 text-[12px] leading-relaxed text-muted-foreground whitespace-pre-wrap break-words font-mono">
                  {content}
                </pre>
              )}
            </>
          )}
      </div>
    </div>
  );
}
