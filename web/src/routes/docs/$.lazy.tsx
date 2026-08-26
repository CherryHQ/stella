import { createLazyFileRoute } from "@tanstack/react-router";
import { Menu, X } from "lucide-react";
import { useState } from "react";
import { SiteHeader } from "@/components/SiteHeader";
import { DocsSidebar } from "@/components/docs/DocsSidebar";
import { mdxComponents } from "@/components/docs/mdx-components";
import { Button } from "@/components/ui/button";
import { getPage, getSidebar } from "@/lib/docs/docs";
import { i18n, type Lang } from "@/lib/docs/i18n";
import { useI18n } from "@/lib/i18n";

export const Route = createLazyFileRoute("/docs/$")({
  component: Page,
});

function Page() {
  const { urlLang, slugs } = Route.useLoaderData();
  const { locale } = useI18n();
  // Explicit URL lang prefix takes priority; otherwise follow the app locale.
  // SAFETY: this route's search is the validated docs URL language schema.
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
        <aside className="hidden md:block w-64 shrink-0 border-r border-border overflow-y-auto p-4">
          <DocsSidebar sections={sidebar} />
        </aside>

        {sidebarOpen && (
          <div className="fixed inset-0 z-40 md:hidden">
            <div
              className="absolute inset-0 bg-foreground/50"
              onClick={() => setSidebarOpen(false)}
            />
            <aside className="absolute left-0 top-0 bottom-0 w-72 bg-background border-r border-border overflow-y-auto p-4">
              <div className="flex justify-end mb-2">
                <Button
                  variant="ghost"
                  size="icon-sm"
                  onClick={() => setSidebarOpen(false)}
                  className="text-muted-foreground hover:text-foreground"
                >
                  <X className="size-5" />
                </Button>
              </div>
              <DocsSidebar sections={sidebar} />
            </aside>
          </div>
        )}

        <main className="flex-1 overflow-y-auto">
          <div className="md:hidden sticky top-0 z-10 flex items-center px-4 h-10 border-b border-border bg-background/80 backdrop-blur-sm">
            <Button
              variant="ghost"
              size="icon-sm"
              onClick={() => setSidebarOpen(true)}
              className="text-muted-foreground hover:text-foreground"
            >
              <Menu className="size-5" />
            </Button>
            <span className="ml-3 text-sm font-medium text-foreground truncate">
              {page.frontmatter?.title}
            </span>
          </div>

          <article className="max-w-3xl mx-auto px-6 py-8 md:px-8">
            {page.frontmatter?.title && (
              <h1 className="text-3xl font-semibold text-foreground mb-2">
                {page.frontmatter.title}
              </h1>
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
