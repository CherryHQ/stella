import { createFileRoute } from "@tanstack/react-router";
import { useEffect, useState, type ReactNode } from "react";

interface ShareMeta {
  title: string;
  mediaType: string;
  expiresAt?: string;
}

export const Route = createFileRoute("/s/$token")({
  component: PublicSharePage,
});

function PublicSharePage() {
  const { token } = Route.useParams();
  const contentUrl = `/api/shares/public/${encodeURIComponent(token)}`;
  const [meta, setMeta] = useState<ShareMeta | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    fetch(contentUrl, { method: "HEAD" })
      .then((res) => {
        if (!res.ok) {
          if (!cancelled) setError("Share not found or expired");
          return;
        }
        const title = res.headers.get("X-Share-Title") ?? "Shared content";
        const mediaType =
          res.headers.get("X-Share-Media-Type") ?? res.headers.get("Content-Type") ?? "";
        const expiresAt = res.headers.get("X-Share-Expires-At") ?? undefined;
        if (!cancelled) setMeta({ title, mediaType, expiresAt });
      })
      .catch(() => {
        if (!cancelled) setError("Share not found or expired");
      });
    return () => {
      cancelled = true;
    };
  }, [contentUrl]);

  if (error) {
    return (
      <PublicShell title="Share unavailable">
        <p className="text-sm text-muted-foreground">{error}</p>
      </PublicShell>
    );
  }
  if (!meta) {
    return (
      <PublicShell title="Loading…">
        <div className="h-4 w-4 animate-spin rounded-full border border-muted-foreground/30 border-t-muted-foreground" />
      </PublicShell>
    );
  }

  const mt = meta.mediaType;
  const isMarkdown = mt.startsWith("text/markdown");
  const isHTML = mt.startsWith("text/html");
  const isImage = mt.startsWith("image/");
  const isPDF = mt === "application/pdf";

  if (isMarkdown || isHTML) {
    return (
      <iframe
        title={meta.title}
        className="h-screen w-full"
        sandbox="allow-scripts allow-forms allow-popups allow-downloads"
        src={contentUrl}
      />
    );
  }

  return (
    <PublicShell title={meta.title} expiresAt={meta.expiresAt} contentUrl={contentUrl}>
      {isImage && (
        <div className="flex justify-center rounded-xl border bg-card p-4">
          <img
            alt={meta.title}
            className="max-h-[calc(100vh-10rem)] max-w-full object-contain"
            src={contentUrl}
          />
        </div>
      )}
      {isPDF && (
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
