import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { createRouter, RouterProvider } from "@tanstack/react-router";
import { QueryClientProvider } from "@tanstack/react-query";
import { routeTree } from "./routeTree.gen";
import { queryClient } from "@/lib/queryClient";
import { I18nProvider } from "@/lib/i18n";
import { applyTheme, getStoredTheme } from "@/lib/theme";
import { recoverFromStaleChunks, registerServiceWorker } from "@/lib/pwa";
import "./globals.css";

if (typeof window !== "undefined") {
  applyTheme(getStoredTheme());
  recoverFromStaleChunks();
  registerServiceWorker();
}

const router = createRouter({
  routeTree,
  context: { queryClient },
});

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}

const root = document.getElementById("app-root");
if (root) {
  createRoot(root).render(
    <StrictMode>
      <I18nProvider>
        <QueryClientProvider client={queryClient}>
          <RouterProvider router={router} />
        </QueryClientProvider>
      </I18nProvider>
    </StrictMode>,
  );
}
