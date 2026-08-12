import { createFileRoute, redirect } from "@tanstack/react-router";
import { adminCompatibilityHref } from "@/lib/admin-routes";

export const Route = createFileRoute("/_app/settings/vision")({
  beforeLoad: ({ location }) => {
    throw redirect({
      href: adminCompatibilityHref(location.pathname, location.searchStr)!,
      replace: true,
    });
  },
});
