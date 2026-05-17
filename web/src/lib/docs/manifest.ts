import { i18n, type Lang } from "./i18n";

const docFiles = import.meta.glob("/content/docs/**/*.{md,mdx}");

function langSuffix(lang: Lang): string {
  return lang === i18n.defaultLanguage ? "" : `.${lang}`;
}

function hasDoc(path: string, lang: Lang): boolean {
  const suffix = langSuffix(lang);
  return [".mdx", ".md"].some((ext) => `/content/docs/${path}${suffix}${ext}` in docFiles);
}

export function hasPage(slugs: string[], lang: Lang): boolean {
  const path = slugs.length === 0 ? "index" : slugs.join("/");
  return (
    hasDoc(path, lang) || (lang !== i18n.defaultLanguage && hasDoc(path, i18n.defaultLanguage))
  );
}
