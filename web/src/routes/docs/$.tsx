import { createFileRoute, notFound } from "@tanstack/react-router";
import { useState } from "react";
import { Menu, X } from "lucide-react";
import { SiteHeader } from "@/components/SiteHeader";
import { DocsSidebar } from "@/components/docs/DocsSidebar";
import { mdxComponents } from "@/components/docs/mdx-components";
import { getPage, getSidebar } from "@/lib/docs/docs";
import { i18n, type Lang } from "@/lib/docs/i18n";
import { useI18n } from "@/lib/i18n";

function parseSplat(splat: string | undefined) {
  const segments = splat?.split("/").filter(Boolean) ?? [];
  if (
    segments[0] &&
    (i18n.languages as readonly string[]).includes(segments[0]) &&
    segments[0] !== i18n.defaultLanguage
  ) {
    return { urlLang: segments[0] as Lang, slugs: segments.slice(1) };
  }
  return { urlLang: null, slugs: segments };
}

export const Route = createFileRoute("/docs/$")({
  component: Page,
  loader: ({ params }) => {
    const { urlLang, slugs } = parseSplat(params._splat);
    // Validate that a page exists in at least one language so we can 404 early.
    const exists =
      getPage(slugs, urlLang ?? i18n.defaultLanguage) ??
      getPage(
        slugs,
        i18n.languages.find((l) => l !== i18n.defaultLanguage) ?? i18n.defaultLanguage,
      );
    if (!exists) throw notFound();
    return { urlLang, slugs };
  },
});

function Page() {
  const { urlLang, slugs } = Route.useLoaderData();
  const { locale } = useI18n();
  // Explicit URL lang prefix takes priority; otherwise follow the app locale.
  const lang = urlLang ?? (locale as Lang);
  // The loader already verified existence; fallback to default lang if locale has no translation.
  const page = (getPage(slugs, lang) ?? getPage(slugs, i18n.defaultLanguage))!;
  const sidebar = getSidebar(lang);
  const MDXContent = page.default;
  const [sidebarOpen, setSidebarOpen] = useState(false);

  return (
    <div className="flex flex-col h-dvh">
      <SiteHeader />
      <div className="flex flex-1 overflow-hidden">
        {/* Desktop sidebar */}
        <aside className="hidden md:block w-64 shrink-0 border-r border-border overflow-y-auto p-4">
          <DocsSidebar groups={sidebar} />
        </aside>

        {/* Mobile sidebar overlay */}
        {sidebarOpen && (
          <div className="fixed inset-0 z-40 md:hidden">
            <div className="absolute inset-0 bg-black/50" onClick={() => setSidebarOpen(false)} />
            <aside className="absolute left-0 top-0 bottom-0 w-72 bg-background border-r border-border overflow-y-auto p-4 shadow-lg">
              <div className="flex justify-end mb-2">
                <button
                  onClick={() => setSidebarOpen(false)}
                  className="p-1.5 rounded-md text-muted-foreground hover:text-foreground hover:bg-accent transition-colors"
                >
                  <X className="size-5" />
                </button>
              </div>
              <DocsSidebar groups={sidebar} />
            </aside>
          </div>
        )}

        {/* Content */}
        <main className="flex-1 overflow-y-auto">
          {/* Mobile sidebar trigger */}
          <div className="md:hidden sticky top-0 z-10 flex items-center px-4 h-10 border-b border-border bg-background/80 backdrop-blur-sm">
            <button
              onClick={() => setSidebarOpen(true)}
              className="p-1.5 rounded-md text-muted-foreground hover:text-foreground hover:bg-accent transition-colors"
            >
              <Menu className="size-5" />
            </button>
            <span className="ml-3 text-sm font-medium text-foreground truncate">
              {page.frontmatter?.title}
            </span>
          </div>

          <article className="max-w-3xl mx-auto px-6 py-8 md:px-8">
            {page.frontmatter?.title && (
              <h1 className="text-3xl font-bold text-foreground mb-2">{page.frontmatter.title}</h1>
            )}
            {page.frontmatter?.description && (
              <p className="text-lg text-muted-foreground mb-8">{page.frontmatter.description}</p>
            )}
            <MDXContent components={mdxComponents} />
          </article>
        </main>
      </div>
    </div>
  );
}
