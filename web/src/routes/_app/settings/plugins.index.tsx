import { createFileRoute, redirect } from "@tanstack/react-router";
import { personalCompatibilityHref } from "@/lib/admin-routes";

export const Route = createFileRoute("/_app/settings/plugins/")({
  beforeLoad: ({ location }) => {
    const href = personalCompatibilityHref(location.pathname, location.searchStr);
    if (href) throw redirect({ href, replace: true });
  },
});
