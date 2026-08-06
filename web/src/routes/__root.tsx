import { createRootRouteWithContext, Outlet } from "@tanstack/react-router";
import type { QueryClient } from "@tanstack/react-query";
import { RouteError, RouteNotFound } from "@/components/RouteFallback";
import { ToastContainer } from "@/hooks/use-toast";

interface RouterContext {
  queryClient: QueryClient;
}

export const Route = createRootRouteWithContext<RouterContext>()({
  // One container for the whole app: toasts are raised by detail panels that are
  // often nested under the page holding the list, and a per-page container only
  // ever saw its own instance's messages.
  component: () => (
    <>
      <Outlet />
      <ToastContainer />
    </>
  ),
  errorComponent: RouteError,
  notFoundComponent: RouteNotFound,
});
