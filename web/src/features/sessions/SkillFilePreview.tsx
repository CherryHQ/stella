import { MarkdownPreview } from "@/components/MarkdownPreview";
import { useI18n } from "@/lib/i18n";
import { cn } from "@/lib/utils";

const codeExtensions = new Set([
  "css",
  "go",
  "html",
  "js",
  "json",
  "jsx",
  "mdx",
  "py",
  "rs",
  "sh",
  "sql",
  "toml",
  "ts",
  "tsx",
  "xml",
  "yaml",
  "yml",
]);

function extensionOf(path: string) {
  const filename = path.split("/").pop() ?? path;
  const index = filename.lastIndexOf(".");
  if (index === -1) return filename.toLowerCase();
  return filename.slice(index + 1).toLowerCase();
}

function fileKind(path: string): "markdown" | "code" | "text" {
  const ext = extensionOf(path);
  if (ext === "md" || ext === "markdown") return "markdown";
  if (codeExtensions.has(ext) || ext === "dockerfile" || ext === "makefile") return "code";
  return "text";
}

interface Props {
  path: string;
  content: string;
  emptyText: string;
  /** Content encoding from the skill file API: "base64" marks a binary file. */
  encoding?: string;
  className?: string;
}

export function SkillFilePreview({ path, content, emptyText, encoding, className }: Props) {
  const { t } = useI18n();
  if (encoding === "base64") {
    return <p className="text-sm text-muted-foreground italic">{t("skills.binaryFile")}</p>;
  }
  if (!content) {
    return <p className="text-sm text-muted-foreground italic">{emptyText}</p>;
  }

  const kind = fileKind(path);

  if (kind === "markdown") {
    return <MarkdownPreview content={content} className={className} />;
  }

  return (
    <pre
      className={cn(
        "max-w-full overflow-auto rounded-lg bg-muted/40 p-4 text-sm leading-relaxed text-foreground font-mono",
        kind === "text"
          ? "whitespace-pre-wrap break-words [overflow-wrap:anywhere]"
          : "whitespace-pre",
        className,
      )}
    >
      {content}
    </pre>
  );
}
