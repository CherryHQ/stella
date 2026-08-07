import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { createRouter, RouterProvider } from "@tanstack/react-router";
import { QueryClientProvider } from "@tanstack/react-query";
import { routeTree } from "./routeTree.gen";
import { RouteError, RoutePending } from "@/components/RouteFallback";
import { queryClient } from "@/lib/queryClient";
import { I18nProvider } from "@/lib/i18n";
import { applyTheme, getStoredTheme } from "@/lib/theme";
import { applyChatWidth, getStoredChatWidth } from "@/lib/chat-width";
import { recoverFromStaleChunks, registerServiceWorker } from "@/lib/pwa";
import { watchBuild } from "@/lib/build-watch";
import "./globals.css";

if (typeof window !== "undefined") {
  applyTheme(getStoredTheme());
  applyChatWidth(getStoredChatWidth());
  recoverFromStaleChunks();
  registerServiceWorker();
  watchBuild();
}

const router = createRouter({
  routeTree,
  context: { queryClient },
  // Blocking loaders (agent detail awaits four queries; the settings agents
  // route fans out six) and 35 lazy chunks used to freeze the previous screen
  // with no signal. 200ms keeps the pulse off fast, cached navigations.
  defaultPendingComponent: RoutePending,
  defaultPendingMs: 200,
  defaultErrorComponent: RouteError,
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
