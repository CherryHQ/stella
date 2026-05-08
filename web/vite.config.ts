import { defineConfig } from "vite-plus";
import mdx from "fumadocs-mdx/vite";
import * as sourceConfig from "./source.config.ts";
import tanstackRouter from "@tanstack/router-plugin/vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import path from "node:path";

export default defineConfig({
  fmt: {
    ignorePatterns: ["src/components/ui/**", ".source"],
  },
  lint: {
    ignorePatterns: ["src/components/ui/**", ".source"],
    options: { typeAware: true, typeCheck: true },
  },
  plugins: [
    mdx(sourceConfig),
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
      collections: path.resolve(import.meta.dirname, ".source"),
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
