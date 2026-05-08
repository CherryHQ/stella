import type { ComponentType } from "react";
import type { Lang } from "./i18n";
import { i18n } from "./i18n";

// -- Types --

interface DocFrontmatter {
  title: string;
  description?: string;
  icon?: string;
}

export interface DocModule {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  default: ComponentType<{ components?: Record<string, any> }>;
  frontmatter: DocFrontmatter;
}

export interface SidebarItem {
  slug: string;
  title: string;
  href: string;
}

export interface SidebarGroup {
  title: string;
  items: SidebarItem[];
}

// -- Glob imports --

const docModules = import.meta.glob<DocModule>("/content/docs/**/*.{md,mdx}", {
  eager: true,
});

const metaFiles = import.meta.glob<{ default: { title: string; pages: string[] } }>(
  "/content/docs/**/meta.json",
  { eager: true },
);

const metaZhFiles = import.meta.glob<{ default: { title: string; pages: string[] } }>(
  "/content/docs/**/meta.zh.json",
  { eager: true },
);

// -- Helpers --

function langSuffix(lang: Lang): string {
  return lang === i18n.defaultLanguage ? "" : `.${lang}`;
}

function findModule(path: string, lang: Lang): DocModule | undefined {
  const suffix = langSuffix(lang);
  for (const ext of [".mdx", ".md"]) {
    const key = `/content/docs/${path}${suffix}${ext}`;
    if (docModules[key]) return docModules[key];
  }
  if (lang !== i18n.defaultLanguage) {
    return findModule(path, i18n.defaultLanguage);
  }
  return undefined;
}

// -- Public API --

export function getPage(slugs: string[], lang: Lang) {
  const path = slugs.length === 0 ? "index" : slugs.join("/");
  return findModule(path, lang);
}

export function getSidebar(lang: Lang): SidebarGroup[] {
  const metas = lang === "zh" ? { ...metaFiles, ...metaZhFiles } : metaFiles;

  const rootMetaKey = lang === "zh" ? "/content/docs/meta.zh.json" : "/content/docs/meta.json";
  const rootMeta = metas[rootMetaKey]?.default;
  if (!rootMeta) return [];

  const groups: SidebarGroup[] = [];

  for (const page of rootMeta.pages) {
    if (page === "index") continue;

    const folderMetaKey =
      lang === "zh" ? `/content/docs/${page}/meta.zh.json` : `/content/docs/${page}/meta.json`;
    const folderMeta = metas[folderMetaKey]?.default;

    if (folderMeta) {
      const items: SidebarItem[] = folderMeta.pages.map((slug) => {
        const mod = findModule(`${page}/${slug}`, lang);
        return {
          slug: `${page}/${slug}`,
          title: mod?.frontmatter?.title ?? formatSlug(slug),
          href: `/docs/${page}/${slug}`,
        };
      });
      groups.push({ title: folderMeta.title, items });
    } else {
      const mod = findModule(page, lang);
      if (mod) {
        groups.push({
          title: mod.frontmatter?.title ?? formatSlug(page),
          items: [
            {
              slug: page,
              title: mod.frontmatter?.title ?? formatSlug(page),
              href: `/docs/${page}`,
            },
          ],
        });
      }
    }
  }

  return groups;
}

function formatSlug(slug: string): string {
  return slug
    .split("-")
    .map((w) => w.charAt(0).toUpperCase() + w.slice(1))
    .join(" ");
}
