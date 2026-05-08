import { useCallback, useEffect, useRef, useState } from "react";
import { Streamdown } from "streamdown";
import { createHighlighter, type Highlighter } from "shiki";
import { ArrowLeft, Pencil, Save, X, Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

const IMAGE_EXTS = new Set(["png", "jpg", "jpeg", "gif", "svg", "webp", "ico", "bmp", "avif"]);

const BINARY_EXTS = new Set([
  "zip",
  "tar",
  "gz",
  "bz2",
  "7z",
  "rar",
  "pdf",
  "woff",
  "woff2",
  "ttf",
  "otf",
  "eot",
  "mp3",
  "mp4",
  "wav",
  "ogg",
  "avi",
  "mov",
  "exe",
  "dll",
  "so",
  "dylib",
  "bin",
  "dat",
  "db",
  "sqlite",
]);

function extOf(path: string): string {
  const dot = path.lastIndexOf(".");
  return dot >= 0 ? path.slice(dot + 1).toLowerCase() : "";
}

function isImage(path: string): boolean {
  return IMAGE_EXTS.has(extOf(path));
}

function isBinary(path: string): boolean {
  return BINARY_EXTS.has(extOf(path));
}

function isMarkdown(lang: string): boolean {
  return lang === "markdown" || lang === "md";
}

let highlighterPromise: Promise<Highlighter> | null = null;

function getHighlighter(): Promise<Highlighter> {
  if (!highlighterPromise) {
    highlighterPromise = createHighlighter({
      themes: ["github-light", "github-dark"],
      langs: [],
    });
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
  sessionID: string;
  onBack: () => void;
  onSave: (content: string) => Promise<void>;
}

export function FileViewer({
  path,
  content,
  language,
  loading,
  saving,
  sessionID,
  onBack,
  onSave,
}: Props) {
  const [editing, setEditing] = useState(false);
  const [editContent, setEditContent] = useState("");
  const [highlighted, setHighlighted] = useState<string>("");
  const [highlightReady, setHighlightReady] = useState(false);
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const ext = extOf(path);
  const fileName = path.split("/").pop() ?? path;

  useEffect(() => {
    setEditing(false);
    setHighlighted("");
    setHighlightReady(false);
  }, [path]);

  useEffect(() => {
    if (loading || editing || isImage(path) || isBinary(path)) return;
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

    (async () => {
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
        <button
          onClick={onBack}
          className="p-1 rounded hover:bg-muted transition-colors text-muted-foreground hover:text-foreground"
        >
          <ArrowLeft className="w-3.5 h-3.5" />
        </button>
        <span className="text-[11px] font-mono text-foreground/80 truncate flex-1 min-w-0">
          {fileName}
        </span>
        {language && (
          <span className="text-[9px] font-mono text-muted-foreground/50 shrink-0">{language}</span>
        )}
        {!loading && !isBinary(path) && !isImage(path) && (
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
                  Save
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
            <Loader2 className="w-4 h-4 animate-spin text-muted-foreground/50" />
          </div>
        )}

        {!loading && isImage(path) && (
          <div className="p-4 flex items-center justify-center h-full">
            <img
              src={`/api/sessions/${encodeURIComponent(sessionID)}/workspace/file-content?path=${encodeURIComponent(path)}&raw=true`}
              alt={fileName}
              className="max-w-full max-h-full object-contain rounded"
              onError={(e) => {
                (e.target as HTMLImageElement).style.display = "none";
              }}
            />
          </div>
        )}

        {!loading && isBinary(path) && (
          <div className="flex flex-col items-center justify-center h-full gap-2 text-muted-foreground/50">
            <span className="text-xs font-mono">Binary file</span>
            <span className="text-[10px] font-mono">.{ext}</span>
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

        {!loading && !isImage(path) && !isBinary(path) && !editing && (
          <>
            {isMarkdown(language) ? (
              <div className="px-4 py-3 prose prose-sm max-w-none text-foreground [&_*]:text-[12px]">
                <Streamdown>{content}</Streamdown>
              </div>
            ) : highlighted ? (
              <div
                className={cn(
                  "text-[12px] leading-relaxed [&_pre]:!bg-transparent [&_pre]:p-3 [&_pre]:m-0 [&_code]:!bg-transparent",
                  "[&_.shiki]:!bg-transparent",
                )}
                dangerouslySetInnerHTML={{ __html: highlighted }}
              />
            ) : highlightReady ? (
              <pre className="p-3 text-[12px] leading-relaxed text-foreground/80 whitespace-pre-wrap break-words font-mono">
                {content}
              </pre>
            ) : (
              <pre className="p-3 text-[12px] leading-relaxed text-foreground/60 whitespace-pre-wrap break-words font-mono">
                {content}
              </pre>
            )}
          </>
        )}
      </div>
    </div>
  );
}
