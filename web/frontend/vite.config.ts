import { defineConfig } from "vite-plus";
import { TanStackRouterVite } from "@tanstack/router-plugin/vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import path from "node:path";

export default defineConfig({
  fmt: {},
  lint: { options: { typeAware: true, typeCheck: true } },
  plugins: [TanStackRouterVite({ routesDirectory: './src/routes', generatedRouteTree: './src/routeTree.gen.ts' }), tailwindcss(), react()],
  base: "/static/dist/",
  build: {
    outDir: "../static/dist",
    emptyOutDir: true,
    manifest: true,
    rollupOptions: {
      input: {
        sessions: path.resolve(import.meta.dirname, "src/entries/sessions.tsx"),
        channels: path.resolve(import.meta.dirname, "src/entries/channels.tsx"),
        scheduler: path.resolve(import.meta.dirname, "src/entries/scheduler.tsx"),
        credentials: path.resolve(import.meta.dirname, "src/entries/credentials.tsx"),
        providers: path.resolve(import.meta.dirname, "src/entries/providers.tsx"),
        users: path.resolve(import.meta.dirname, "src/entries/users.tsx"),
        plugins: path.resolve(import.meta.dirname, "src/entries/plugins.tsx"),
        agents: path.resolve(import.meta.dirname, "src/entries/agents.tsx"),
        account: path.resolve(import.meta.dirname, "src/entries/account.tsx"),
        login: path.resolve(import.meta.dirname, "src/entries/login.tsx"),
      },
    },
  },
  resolve: {
    alias: {
      "@": path.resolve(import.meta.dirname, "src"),
    },
  },
  server: {
    port: 5173,
    strictPort: true,
  },
});
