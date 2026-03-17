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
    port: 5173,
  },
  build: {
    sourcemap: true,
  },
  preview: {
    port: 4173,
  },
  resolve: {
    tsconfigPaths: true,
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
