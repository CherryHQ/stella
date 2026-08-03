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
    ignorePatterns: ["src/components/ui/**", "src/lib/api-client/**", "src/routeTree.gen.ts"],
  },
  lint: {
    ignorePatterns: ["src/components/ui/**", "src/lib/api-client/**", "src/routeTree.gen.ts"],
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
