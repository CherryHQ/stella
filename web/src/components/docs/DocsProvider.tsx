import type { ReactNode } from "react";
import { RootProvider } from "fumadocs-ui/provider/tanstack";
import { defineI18nUI } from "fumadocs-ui/i18n";
import { i18n } from "@/lib/docs/i18n";

const { provider } = defineI18nUI(i18n, {
  translations: {
    en: {
      displayName: "English",
    },
    zh: {
      displayName: "中文",
      search: "搜索",
      searchNoResult: "未找到结果",
      toc: "目录",
      tocNoHeadings: "无标题",
      lastUpdate: "最后更新于",
      chooseLanguage: "选择语言",
      nextPage: "下一页",
      previousPage: "上一页",
      chooseTheme: "主题",
      editOnGithub: "在 GitHub 上编辑",
    },
  },
});

export function DocsProvider({
  lang = i18n.defaultLanguage,
  children,
}: { lang?: string; children: ReactNode }) {
  return <RootProvider i18n={provider(lang)}>{children}</RootProvider>;
}
