import type { BaseLayoutProps } from 'fumadocs-ui/layouts/shared';
import { i18n } from '@/lib/i18n';
import { t } from '@/lib/translations';

export const gitConfig = {
  user: 'vaayne',
  repo: 'anna',
  branch: 'main',
};

export function baseOptions(locale: string = i18n.defaultLanguage): BaseLayoutProps {
  const tr = t(locale);
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
      { text: tr.about, url: `/${locale}/about` },
      { text: tr.docs, url: `/${locale}/docs` },
    ],
    githubUrl: `https://github.com/${gitConfig.user}/${gitConfig.repo}`,
  };
}
