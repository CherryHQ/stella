import { cloudflare } from '@cloudflare/vite-plugin';
import tailwindcss from '@tailwindcss/vite';
import { tanstackStart } from '@tanstack/react-start/plugin/vite';
import react from '@vitejs/plugin-react';
import mdx from 'fumadocs-mdx/vite';
import * as sourceConfig from './source.config.ts';
import { defineConfig } from 'vite-plus';

export default defineConfig({
  fmt: {
    ignorePatterns: ['dist/**', 'build/**', 'node_modules/**', 'src/routeTree.gen.ts'],
    singleQuote: true,
    semi: true,
    sortPackageJson: true,
  },
  lint: {
    ignorePatterns: ['dist/**', 'build/**', 'node_modules/**', 'src/routeTree.gen.ts'],
    options: {
      typeAware: true,
      typeCheck: true,
    },
    rules: {
      'no-console': ['error', { allow: ['error'] }],
    },
  },
  server: {
    port: 3000,
  },
  build: {
    sourcemap: true,
  },
  preview: {
    port: 4173,
  },
  resolve: {
    tsconfigPaths: true,
    dedupe: ['react', 'react-dom'],
  },
  environments: {
    ssr: {
      optimizeDeps: {
        include: [
          'react',
          'react-dom',
          'react-dom/server',
          'react/jsx-runtime',
          'react/jsx-dev-runtime',
          'fumadocs-ui/layouts/home',
          'fumadocs-ui/layouts/docs',
          'fumadocs-ui/layouts/docs/page',
          'fumadocs-ui/layouts/home/not-found',
          'fumadocs-ui/provider/tanstack',
          'fumadocs-ui/mdx',
          'fumadocs-ui/components/card',
          'fumadocs-ui/components/dialog/search-default',
          'fumadocs-ui/i18n',
        ],
      },
    },
  },
  plugins: [
    mdx(sourceConfig),
    tailwindcss(),
    cloudflare({ viteEnvironment: { name: 'ssr' } }),
    tanstackStart({
      prerender: {
        enabled: true,
      },
    }),
    react(),
  ],
});
