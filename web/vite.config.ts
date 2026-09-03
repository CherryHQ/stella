import { defineConfig } from "vite-plus";
import mdx from "@mdx-js/rollup";
import remarkGfm from "remark-gfm";
import remarkFrontmatter from "remark-frontmatter";
import remarkMdxFrontmatter from "remark-mdx-frontmatter";
import tanstackRouter from "@tanstack/router-plugin/vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import fs from "node:fs";
import path from "node:path";

// pnpm rewrites node_modules/.modules.yaml whenever it (re)links node_modules,
// e.g. after flipping enableGlobalVirtualStore. Vite keys its dep cache on the
// lockfile and this config only, so a cache built for the old layout survives,
// and the next incremental re-optimization bundles React from both layouts:
// "Invalid hook call ... more than one copy of React". Rebuild the cache
// whenever pnpm relinked after it was written.
function depCacheOutdated(): boolean {
  const mtime = (file: string): number => {
    try {
      return fs.statSync(path.join(import.meta.dirname, file)).mtimeMs;
    } catch {
      return 0;
    }
  };
  const cached = mtime("node_modules/.vite/deps/_metadata.json");
  return cached > 0 && mtime("node_modules/.modules.yaml") > cached;
}

export default defineConfig({
  fmt: {
    ignorePatterns: [
      "src/components/ui/**",
      "src/lib/api-client/**",
      "src/routeTree.gen.ts",
      ".agents/**",
      ".pi/**",
      "tools/oxlint/anti-slop/**",
    ],
  },
  lint: {
    ignorePatterns: [
      "src/components/ui/**",
      "src/lib/api-client/**",
      "src/routeTree.gen.ts",
      ".agents/**",
      ".pi/**",
      "tools/oxlint/anti-slop/**",
    ],
    jsPlugins: [{ name: "anti-slop", specifier: "./tools/oxlint/anti-slop/index.ts" }],
    rules: {
      // Zero-finding rules can be enforced now without breaking the mandatory `mise format` / `vp check --fix` gate.
      // Slop-heavy rules are at `warn` (visible, non-blocking); promote to `error` after each finding group is migrated.
      "anti-slop/no-object-parameters": "error",
      "anti-slop/no-reflect-apply": "error",
      "anti-slop/no-reflect-get": "error",
      "anti-slop/no-shape-in-symbol-names": "error",
      "anti-slop/no-unknown-type-aliases": "error",
      "anti-slop/no-widen-then-assert": "error",
      "anti-slop/no-chained-type-assertions": "warn",
      "anti-slop/no-conditional-empty-object-spread": "warn",
      "anti-slop/no-known-value-widening": "warn",
      "anti-slop/no-module-mocking": "warn",
      "anti-slop/no-runtime-typeof": ["warn", { allowInTypeGuards: true }],
      "anti-slop/no-unknown-parameters": "warn",
      "anti-slop/no-unknown-returns": "warn",
      "anti-slop/no-unsafe-dictionary-type": "warn",
      "anti-slop/require-safety-comment-for-type-assertion": "warn",
    },
    options: { typeAware: true, typeCheck: true },
  },
  plugins: [
    mdx({
      remarkPlugins: [remarkGfm, remarkFrontmatter, remarkMdxFrontmatter],
    }),
    tanstackRouter({
      routesDirectory: "./src/routes",
      generatedRouteTree: "./src/routeTree.gen.ts",
    }),
    tailwindcss(),
    react(),
  ],
  base: "/",
  build: {
    outDir: "./static/dist",
    emptyOutDir: true,
    chunkSizeWarningLimit: 800,
  },
  optimizeDeps: {
    force: depCacheOutdated(),
  },
  resolve: {
    // Base UI's deep imports must resolve React through the app's shared runtime.
    dedupe: ["react", "react-dom"],
    alias: {
      "@": path.resolve(import.meta.dirname, "src"),
      "node:path": "pathe",
    },
  },
  server: {
    port: 25688,
    strictPort: true,
    proxy: {
      "/api": "http://localhost:25678",
      "/api-references": "http://localhost:25678",
      "/auth": "http://localhost:25678",
      "/oidc": "http://localhost:25678",
      "/webhooks": "http://localhost:25678",
    },
  },
});
