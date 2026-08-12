import { createFileRoute, redirect } from "@tanstack/react-router";
import { adminCompatibilityHref } from "@/lib/admin-routes";
import { meQueryOptions } from "@/lib/queries/me";

export const Route = createFileRoute("/_app/settings/about")({
  beforeLoad: async ({ context: { queryClient }, location }) => {
    const me = await queryClient.ensureQueryData(meQueryOptions);
    if (me?.is_admin) {
      throw redirect({
        href: adminCompatibilityHref(location.pathname, location.searchStr)!,
        replace: true,
      });
    }
  },
});
