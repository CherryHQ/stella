import { createFileRoute, redirect } from "@tanstack/react-router";
import { meQueryOptions } from "@/lib/queries/me";

export const Route = createFileRoute("/_app")({
  loader: async ({ context: { queryClient } }) => {
    try {
      const me = await queryClient.ensureQueryData(meQueryOptions);
      if (me.needs_onboarding) {
        throw redirect({ to: "/onboarding" });
      }
      return me;
    } catch (e) {
      if ((e as any)?.isRedirect) throw e;
      throw redirect({ to: "/login" });
    }
  },
});
