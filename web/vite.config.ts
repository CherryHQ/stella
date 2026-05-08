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
    ignorePatterns: ["src/components/ui/**", "content/**"],
  },
  lint: {
    ignorePatterns: ["src/components/ui/**", "content/**"],
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
  },
  resolve: {
    alias: {
      "@": path.resolve(import.meta.dirname, "src"),
      "node:path": "pathe",
    },
  },
  server: {
    port: 5173,
    strictPort: true,
    proxy: {
      "/api": "http://localhost:25678",
    },
  },
});
