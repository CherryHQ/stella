import { createFileRoute, notFound } from "@tanstack/react-router";
import { i18n, type Lang } from "@/lib/docs/i18n";
import { hasPage } from "@/lib/docs/manifest";

function parseSplat(splat: string | undefined) {
  const segments = splat?.split("/").filter(Boolean) ?? [];
  if (
    // SAFETY: languages is the app's supported-lang list; membership was checked via .includes.
    segments[0] &&
    (i18n.languages as readonly string[]).includes(segments[0]) &&
    segments[0] !== i18n.defaultLanguage
  ) {
    // SAFETY: segments[0] was confirmed to be one of i18n.languages above.
    return { urlLang: segments[0] as Lang, slugs: segments.slice(1) };
  }
  return { urlLang: null, slugs: segments };
}

export const Route = createFileRoute("/docs/$")({
  loader: ({ params }) => {
    const { urlLang, slugs } = parseSplat(params._splat);
    // Validate that a page exists in at least one language so we can 404 early.
    const exists =
      hasPage(slugs, urlLang ?? i18n.defaultLanguage) ||
      hasPage(
        slugs,
        i18n.languages.find((l) => l !== i18n.defaultLanguage) ?? i18n.defaultLanguage,
      );
    if (!exists) throw notFound();
    return { urlLang, slugs };
  },
});
