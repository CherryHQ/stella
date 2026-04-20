import { createRootRoute, HeadContent, Outlet, Scripts, useParams } from '@tanstack/react-router';
import { RootProvider } from 'fumadocs-ui/provider/tanstack';
import { defineI18nUI } from 'fumadocs-ui/i18n';
import appCss from '@/styles/app.css?url';
import { i18n } from '@/lib/i18n';

export const Route = createRootRoute({
  head: () => ({
    meta: [
      {
        charSet: 'utf-8',
      },
      {
        name: 'viewport',
        content: 'width=device-width, initial-scale=1',
      },
      {
        title: 'anna docs',
      },
    ],
    links: [
      { rel: 'stylesheet', href: appCss },
      { rel: 'icon', href: '/favicon.ico', sizes: '48x48' },
      {
        rel: 'icon',
        type: 'image/png',
        href: '/favicon-16x16.png',
        sizes: '16x16',
      },
      {
        rel: 'icon',
        type: 'image/png',
        href: '/favicon-32x32.png',
        sizes: '32x32',
      },
      {
        rel: 'icon',
        type: 'image/svg+xml',
        href: '/anna-monogram.svg',
      },
      { rel: 'apple-touch-icon', href: '/apple-touch-icon.png' },
      { rel: 'manifest', href: '/site.webmanifest' },
    ],
  }),
  component: RootComponent,
});

const { provider } = defineI18nUI(i18n, {
  translations: {
    en: {
      displayName: 'English',
    },
    zh: {
      displayName: '中文',
      search: '搜索',
      searchNoResult: '未找到结果',
      toc: '目录',
      tocNoHeadings: '无标题',
      lastUpdate: '最后更新于',
      chooseLanguage: '选择语言',
      nextPage: '下一页',
      previousPage: '上一页',
      chooseTheme: '主题',
      editOnGithub: '在 GitHub 上编辑',
    },
  },
});

function RootComponent() {
  const { lang = i18n.defaultLanguage } = useParams({ strict: false });

  return (
    <html lang={lang} suppressHydrationWarning>
      <head>
        <HeadContent />
      </head>
      <body className="flex flex-col min-h-screen">
        <RootProvider i18n={provider(lang)}>
          <Outlet />
        </RootProvider>
        <Scripts />
      </body>
    </html>
  );
}
