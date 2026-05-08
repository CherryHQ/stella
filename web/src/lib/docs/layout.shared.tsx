import type { BaseLayoutProps } from "fumadocs-ui/layouts/shared";
import { i18n } from "@/lib/docs/i18n";
import { t } from "@/lib/docs/translations";

export function baseOptions(): BaseLayoutProps {
  const tr = t(i18n.defaultLanguage);
  return {
    i18n,
    nav: {
      title: (
        <>
          <img src="/anna-monogram.svg" alt="anna" width={24} height={24} className="rounded-sm" />
          anna
        </>
      ),
    },
    links: [
      { text: tr.about, url: "/about" },
      { text: tr.docs, url: "/docs" },
    ],
    githubUrl: "https://github.com/vaayne/anna",
  };
}
