import { Streamdown } from "streamdown";
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
  className?: string;
}

export function SkillFilePreview({ path, content, emptyText, className }: Props) {
  if (!content) {
    return <p className="text-sm text-muted-foreground italic">{emptyText}</p>;
  }

  const kind = fileKind(path);

  if (kind === "markdown") {
    return (
      <div
        className={cn(
          "prose prose-sm max-w-none min-w-0 text-foreground dark:prose-invert",
          "[&_*]:min-w-0 [&_a]:break-words [&_code]:break-words [&_pre]:max-w-full [&_pre]:overflow-x-auto [&_table]:block [&_table]:max-w-full [&_table]:overflow-x-auto",
          className,
        )}
      >
        <Streamdown>{content}</Streamdown>
      </div>
    );
  }

  return (
    <pre
      className={cn(
        "max-w-full overflow-auto rounded-lg bg-muted/40 p-4 text-sm leading-relaxed text-foreground/90 font-mono",
        kind === "text" ? "whitespace-pre-wrap break-words" : "whitespace-pre",
        className,
      )}
    >
      {content}
    </pre>
  );
}
