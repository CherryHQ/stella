import { redirect } from '@tanstack/react-router';
import { createMiddleware, createStart } from '@tanstack/react-start';
import { rewritePath } from 'fumadocs-core/negotiation';
import { i18n } from '@/lib/i18n';

const llmPath = rewritePath('/docs{/*path}.mdx', '/llms.mdx/docs{/*path}');
const rewriteLLM = (pathname: string) => llmPath.rewrite(pathname);

const middleware = createMiddleware().server(({ next, request }) => {
  const url = new URL(request.url);

  // Rewrite .mdx requests to LLM endpoints
  const path = rewriteLLM(url.pathname);
  if (path) {
    throw redirect(new URL(path, url));
  }

  // Redirect bare `/` to default locale
  if (url.pathname === '/') {
    throw redirect(new URL(`/${i18n.defaultLanguage}`, url));
  }

  // Redirect old `/docs/...` and `/about` paths to default locale
  const segment = url.pathname.split('/')[1];
  const knownRoots = new Set(['api', 'llms.txt', 'llms-full.txt', 'llms.mdx']);
  if (
    segment &&
    !(i18n.languages as readonly string[]).includes(segment) &&
    !knownRoots.has(segment)
  ) {
    throw redirect(new URL(`/${i18n.defaultLanguage}${url.pathname}`, url));
  }

  return next();
});

export const startInstance = createStart(() => {
  return {
    requestMiddleware: [middleware],
  };
});
