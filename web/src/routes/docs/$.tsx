import { createFileRoute, notFound } from "@tanstack/react-router";
import browserCollections from "collections/browser";
import { DocsLayout } from "fumadocs-ui/layouts/docs";
import {
  DocsBody,
  DocsDescription,
  DocsPage,
  DocsTitle,
} from "fumadocs-ui/layouts/docs/page";
import { Suspense } from "react";
import { useMDXComponents } from "@/components/docs/mdx";
import { DocsProvider } from "@/components/docs/DocsProvider";
import { baseOptions } from "@/lib/docs/layout.shared";
import { i18n } from "@/lib/docs/i18n";
import { source } from "@/lib/docs/source";

type Lang = (typeof i18n)["languages"][number];

function parseLang(splat: string | undefined) {
  const segments = splat?.split("/").filter(Boolean) ?? [];
  if (segments[0] && i18n.languages.includes(segments[0] as Lang) && segments[0] !== i18n.defaultLanguage) {
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

function Page() {
  const { lang, path, pageTree } = Route.useLoaderData();

  return (
    <DocsProvider lang={lang}>
      <DocsLayout {...baseOptions()} tree={pageTree}>
        <Suspense>{clientLoader.useContent(path)}</Suspense>
      </DocsLayout>
    </DocsProvider>
  );
}
