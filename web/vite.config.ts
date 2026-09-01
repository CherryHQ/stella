import { defineConfig } from "vite-plus";
import mdx from "@mdx-js/rollup";
import remarkGfm from "remark-gfm";
import remarkFrontmatter from "remark-frontmatter";
import remarkMdxFrontmatter from "remark-mdx-frontmatter";
import tanstackRouter from "@tanstack/router-plugin/vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import path from "node:path";

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
  resolve: {
    // Rolldown's dependency optimizer bundles Base UI's peer React into every
    // optimized deep import. Leave Base UI as ESM so its hooks use the app's React.
    dedupe: ["react", "react-dom"],
    alias: {
      "@": path.resolve(import.meta.dirname, "src"),
      "node:path": "pathe",
    },
  },
  optimizeDeps: {
    exclude: ["@base-ui/react"],
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
