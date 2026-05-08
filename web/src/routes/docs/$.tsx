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

export const Route = createFileRoute("/docs/$")({
  component: Page,
  loader: async ({ params }) => {
    const slugs = params._splat?.split("/").filter(Boolean) ?? [];
    const page = source.getPage(slugs, i18n.defaultLanguage);
    if (!page) throw notFound();

    await clientLoader.preload(page.path);
    return {
      path: page.path,
      pageTree: source.getPageTree(i18n.defaultLanguage),
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
  const { path, pageTree } = Route.useLoaderData();

  return (
    <DocsProvider>
      <DocsLayout {...baseOptions()} tree={pageTree}>
        <Suspense>{clientLoader.useContent(path)}</Suspense>
      </DocsLayout>
    </DocsProvider>
  );
}
