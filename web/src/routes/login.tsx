import { createFileRoute, redirect } from "@tanstack/react-router";
import { meQueryOptions } from "@/lib/queries/me";

export const Route = createFileRoute("/login")({
  beforeLoad: async ({ context: { queryClient } }) => {
    try {
      const me = await queryClient.ensureQueryData(meQueryOptions);
      if (me.needs_onboarding) {
        throw redirect({ to: "/onboarding" });
      }
      throw redirect({ to: "/sessions" as any });
    } catch (e) {
      if ((e as any)?.isRedirect) throw e;
      // not authenticated — render login
    }
  },
});
