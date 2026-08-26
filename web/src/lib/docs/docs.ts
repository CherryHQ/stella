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

export interface SidebarSection {
  title?: string;
  groups: SidebarGroup[];
}

// -- Glob imports --

const docModules = import.meta.glob<DocModule>("/content/docs/**/*.{md,mdx}", {
  eager: true,
});

interface FolderMeta {
  title: string;
  pages: string[];
}

interface RootMeta {
  title: string;
  sections: Array<{ title?: string; pages: string[] }>;
}

type MetaModule = { default: FolderMeta | RootMeta };

const metaFiles = import.meta.glob<MetaModule>("/content/docs/**/meta.json", { eager: true });

const metaZhFiles = import.meta.glob<MetaModule>("/content/docs/**/meta.zh.json", { eager: true });

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

function formatSlug(slug: string): string {
  return slug
    .split("-")
    .map((w) => w.charAt(0).toUpperCase() + w.slice(1))
    .join(" ");
}

function getMetas(lang: Lang) {
  return lang === "zh" ? { ...metaFiles, ...metaZhFiles } : metaFiles;
}

function getFolderMeta(
  dir: string,
  lang: Lang,
  metas: Record<string, MetaModule>,
): FolderMeta | undefined {
  const key =
    lang === "zh" ? `/content/docs/${dir}/meta.zh.json` : `/content/docs/${dir}/meta.json`;
  const meta = metas[key]?.default;
  // SAFETY: meta has pages (not sections), which is the FolderMeta shape.
  if (meta && "pages" in meta && !("sections" in meta))
    // SAFETY: meta has pages (not sections), which is the FolderMeta shape.
    return meta as FolderMeta;
  return undefined;
}

function buildGroup(
  dir: string,
  lang: Lang,
  metas: Record<string, MetaModule>,
): SidebarGroup | undefined {
  const folderMeta = getFolderMeta(dir, lang, metas);

  if (folderMeta) {
    const items: SidebarItem[] = folderMeta.pages.map((slug) => {
      const mod = findModule(`${dir}/${slug}`, lang);
      return {
        slug: `${dir}/${slug}`,
        title: mod?.frontmatter?.title ?? formatSlug(slug),
        href: `/docs/${dir}/${slug}`,
      };
    });
    return { title: folderMeta.title, items };
  }

  const mod = findModule(dir, lang);
  if (mod) {
    return {
      title: mod.frontmatter?.title ?? formatSlug(dir),
      items: [
        {
          slug: dir,
          title: mod.frontmatter?.title ?? formatSlug(dir),
          href: `/docs/${dir}`,
        },
      ],
    };
  }

  return undefined;
}

// -- Public API --

export function getPage(slugs: string[], lang: Lang) {
  const path = slugs.length === 0 ? "index" : slugs.join("/");
  return findModule(path, lang);
}

export function getSidebar(lang: Lang): SidebarSection[] {
  const metas = getMetas(lang);

  const rootMetaKey = lang === "zh" ? "/content/docs/meta.zh.json" : "/content/docs/meta.json";
  // SAFETY: the root meta navigator always carries sections, shape RootMeta.
  const rootMeta = metas[rootMetaKey]?.default as RootMeta | undefined;
  if (!rootMeta?.sections) return [];

  return rootMeta.sections
    .map((section) => {
      const groups: SidebarGroup[] = [];
      for (const page of section.pages) {
        if (page === "index") continue;
        const group = buildGroup(page, lang, metas);
        if (group) groups.push(group);
      }
      return { title: section.title, groups };
    })
    .filter((s) => s.groups.length > 0);
}
