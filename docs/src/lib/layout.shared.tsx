import type { BaseLayoutProps } from 'fumadocs-ui/layouts/shared';
import { i18n } from '@/lib/i18n';

export const gitConfig = {
  user: 'vaayne',
  repo: 'anna',
  branch: 'main',
};

export function baseOptions(locale: string = i18n.defaultLanguage): BaseLayoutProps {
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
      { text: 'About', url: `/${locale}/about` },
      { text: 'Docs', url: `/${locale}/docs` },
    ],
    githubUrl: `https://github.com/${gitConfig.user}/${gitConfig.repo}`,
  };
}
