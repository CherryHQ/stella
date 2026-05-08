import { createFileRoute, notFound } from "@tanstack/react-router";
import browserCollections from "collections/browser";
import { DocsLayout } from "fumadocs-ui/layouts/docs";
import { SidebarTrigger } from "fumadocs-ui/components/sidebar/base";
import { DocsBody, DocsDescription, DocsPage, DocsTitle } from "fumadocs-ui/layouts/docs/page";
import { Menu } from "lucide-react";
import { Suspense } from "react";
import { useMDXComponents } from "@/components/docs/mdx";
import { DocsProvider } from "@/components/docs/DocsProvider";
import { SiteHeader } from "@/components/SiteHeader";
import { baseOptions } from "@/lib/docs/layout.shared";
import { i18n } from "@/lib/docs/i18n";
import { source } from "@/lib/docs/source";

type Lang = (typeof i18n)["languages"][number];

function parseLang(splat: string | undefined) {
  const segments = splat?.split("/").filter(Boolean) ?? [];
  if (
    segments[0] &&
    i18n.languages.includes(segments[0] as Lang) &&
    segments[0] !== i18n.defaultLanguage
  ) {
    return { lang: segments[0] as Lang, slugs: segments.slice(1) };
  }
  return { lang: i18n.defaultLanguage, slugs: segments };
}

export const Route = createFileRoute("/docs/$")({
  component: Page,
  loader: async ({ params }) => {
    const { lang, slugs } = parseLang(params._splat);
    const page = source.getPage(slugs, lang);
    if (!page) throw notFound();

    await clientLoader.preload(page.path);
    return {
      lang,
      path: page.path,
      pageTree: source.getPageTree(lang),
    };
  },
});

const clientLoader = browserCollections.docs.createClientLoader({
  component({ toc, frontmatter, default: MDX }) {
    return (
      <DocsPage toc={toc}>
        <DocsTitle>{frontmatter.title}</DocsTitle>
        <DocsDescription>{frontmatter.description}</DocsDescription>
        <DocsBody>
          {/* biome-ignore lint/correctness/useHookAtTopLevel: fumadocs clientLoader component pattern */}
          <MDX components={useMDXComponents()} />
        </DocsBody>
      </DocsPage>
    );
  },
});

function DocsMobileNav() {
  return (
    <div className="[grid-area:header] flex items-center px-4 h-10 md:hidden border-b bg-fd-background/80 backdrop-blur-sm">
      <SidebarTrigger className="p-1.5 rounded-md text-fd-muted-foreground hover:bg-fd-accent hover:text-fd-foreground transition-colors">
        <Menu className="size-5" />
      </SidebarTrigger>
    </div>
  );
}

function Page() {
  const { lang, path, pageTree } = Route.useLoaderData();
  const opts = baseOptions();

  return (
    <>
      <SiteHeader variant="docs" />
      <DocsProvider lang={lang}>
        <DocsLayout
          {...opts}
          tree={pageTree}
          nav={{ ...opts.nav, component: <DocsMobileNav /> }}
          containerProps={{
            className: "max-md:[--fd-header-height:2.5rem]",
            style: { "--fd-docs-height": "calc(100dvh - 3.5rem)" } as React.CSSProperties,
          }}
        >
          <Suspense>{clientLoader.useContent(path)}</Suspense>
        </DocsLayout>
      </DocsProvider>
    </>
  );
}
