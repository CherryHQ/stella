import { createFileRoute } from "@tanstack/react-router";
import { useEffect, useMemo, useState, type ReactNode } from "react";

interface ShareMeta {
  title: string;
  mediaType: string;
  expiresAt?: string;
}

export const Route = createFileRoute("/s/$token")({
  component: PublicArtifactSharePage,
});

function PublicArtifactSharePage() {
  const { token } = Route.useParams();
  const contentUrl = `/api/public/artifact-shares/${encodeURIComponent(token)}`;
  const [meta, setMeta] = useState<ShareMeta | null>(null);
  const [markdown, setMarkdown] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    fetch(contentUrl)
      .then((res) => {
        if (!res.ok) {
          if (!cancelled) setError("Artifact not found");
          return;
        }
        const title = res.headers.get("X-Share-Title") ?? "Shared artifact";
        const mediaType = res.headers.get("Content-Type") ?? "";
        const expiresAt = res.headers.get("X-Share-Expires-At") ?? undefined;
        if (!cancelled) setMeta({ title, mediaType, expiresAt });

        if (mediaType.startsWith("text/markdown")) {
          res.text().then((text) => {
            if (!cancelled) setMarkdown(text);
          });
        }
      })
      .catch(() => {
        if (!cancelled) setError("Artifact not found");
      });
    return () => {
      cancelled = true;
    };
  }, [contentUrl]);

  const renderedMarkdown = useMemo(
    () => (markdown == null ? "" : renderSafeMarkdown(markdown)),
    [markdown],
  );

  if (error) {
    return (
      <PublicShell title="Artifact unavailable">
        <p className="text-sm text-muted-foreground">{error}</p>
      </PublicShell>
    );
  }
  if (!meta) {
    return (
      <PublicShell title="Loading artifact…">
        <div className="h-4 w-4 animate-spin rounded-full border border-muted-foreground/30 border-t-muted-foreground" />
      </PublicShell>
    );
  }

  const mt = meta.mediaType;

  return (
    <PublicShell title={meta.title} expiresAt={meta.expiresAt} contentUrl={contentUrl}>
      {mt.startsWith("text/html") && (
        <iframe
          title={meta.title}
          className="h-[calc(100vh-8rem)] w-full rounded-xl border bg-white"
          sandbox="allow-scripts allow-forms allow-popups allow-downloads"
          src={contentUrl}
        />
      )}
      {mt.startsWith("text/markdown") && (
        <article
          className="prose prose-neutral dark:prose-invert mx-auto max-w-3xl rounded-xl border bg-card p-6"
          dangerouslySetInnerHTML={{ __html: renderedMarkdown }}
        />
      )}
      {mt.startsWith("image/") && (
        <div className="flex justify-center rounded-xl border bg-card p-4">
          <img
            alt={meta.title}
            className="max-h-[calc(100vh-10rem)] max-w-full object-contain"
            src={contentUrl}
          />
        </div>
      )}
      {mt === "application/pdf" && (
        <iframe
          title={meta.title}
          className="h-[calc(100vh-8rem)] w-full rounded-xl border bg-card"
          sandbox="allow-scripts allow-same-origin"
          src={contentUrl}
        />
      )}
    </PublicShell>
  );
}

function PublicShell({
  title,
  expiresAt,
  contentUrl,
  children,
}: {
  title: string;
  expiresAt?: string;
  contentUrl?: string;
  children: ReactNode;
}) {
  return (
    <main className="min-h-screen bg-background text-foreground">
      <header className="sticky top-0 z-10 border-b bg-background/90 backdrop-blur">
        <div className="mx-auto flex max-w-6xl items-center justify-between gap-4 px-4 py-3">
          <div className="min-w-0">
            <h1 className="truncate text-sm font-semibold">{title}</h1>
            <p className="text-xs text-muted-foreground">
              Shared with Stella{expiresAt ? ` · Expires ${expiresAt}` : " · No expiration"}
            </p>
          </div>
          {contentUrl && (
            <a
              className="rounded-lg border border-input bg-popover px-3 py-1.5 text-sm shadow-xs/5 hover:bg-accent/50"
              href={contentUrl}
              download
            >
              Download
            </a>
          )}
        </div>
      </header>
      <div className="mx-auto max-w-6xl p-4">{children}</div>
    </main>
  );
}

function renderSafeMarkdown(input: string): string {
  const escaped = input
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#39;");
  return escaped
    .split(/\n{2,}/)
    .map((block) => {
      const lines = block.split("\n");
      if (lines[0]?.startsWith("# ")) return `<h1>${inlineMarkdown(lines[0].slice(2))}</h1>`;
      if (lines[0]?.startsWith("## ")) return `<h2>${inlineMarkdown(lines[0].slice(3))}</h2>`;
      if (lines.every((line) => line.startsWith("- "))) {
        return `<ul>${lines.map((line) => `<li>${inlineMarkdown(line.slice(2))}</li>`).join("")}</ul>`;
      }
      return `<p>${inlineMarkdown(lines.join("<br />"))}</p>`;
    })
    .join("\n");
}

function inlineMarkdown(input: string): string {
  return input
    .replace(/`([^`]+)`/g, "<code>$1</code>")
    .replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>")
    .replace(/\*([^*]+)\*/g, "<em>$1</em>");
}
