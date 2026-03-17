import { createFileRoute, notFound } from '@tanstack/react-router';
import { getLLMText, source } from '@/lib/source';

export const Route = createFileRoute('/llms.mdx/docs/$')({
  server: {
    handlers: {
      GET: async ({ params, request }) => {
        const slugs = params._splat?.split('/') ?? [];
        const lang = new URL(request.url).searchParams.get('lang') ?? undefined;
        const page = source.getPage(slugs, lang);
        if (!page) throw notFound();

        return new Response(await getLLMText(page), {
          headers: {
            'Content-Type': 'text/markdown',
          },
        });
      },
    },
  },
});
