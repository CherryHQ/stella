import { createFileRoute } from '@tanstack/react-router';
import { createFromSource } from 'fumadocs-core/search/server';
import { createTokenizer } from '@orama/tokenizers/mandarin';
import { source } from '@/lib/source';

const cjkTokenizer = { tokenizer: createTokenizer() };

const server = createFromSource(source, {
  localeMap: {
    en: 'english',
    zh: cjkTokenizer,
  },
});

export const Route = createFileRoute('/api/search')({
  server: {
    handlers: {
      GET: async ({ request }) => server.GET(request),
    },
  },
});
